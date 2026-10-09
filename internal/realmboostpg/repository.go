// SPDX-License-Identifier: AGPL-3.0-only
package realmboostpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

type Commerce interface {
	RealmBoostRequest(context.Context, *sql.DB, *sql.Tx, string, string, string, string) (string, error)
	CreateRealmBoostOrder(context.Context, *sql.DB, *sql.Tx, string, string, string, string, string, int64, wallet.Amount, time.Time, time.Time) (c.Order, error)
	RealmBoostPerkLevel(context.Context, *sql.DB, *sql.Tx, string, map[string]int, time.Time) (int, error)
}

// OwnedPolicy is mandatory. Label cap values and subscription-to-perk selection
// are unverified upstream policy; never silently assign guessed defaults.
// An explicitly empty PerkLevels map selects base XP without subscription bonus.
type OwnedPolicy struct {
	LabelCaps  [4]int
	PerkLevels map[string]int
}
type Config struct {
	TreasuryAccountID, TreasuryWalletID string
	Policy                              *OwnedPolicy
	OrderTTL                            time.Duration
	Now                                 func() time.Time
}
type Repository struct {
	db       *sql.DB
	guard    *authoritypg.TransactionGuard
	commerce Commerce
	cfg      Config
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, commerce Commerce, cfg Config) (*Repository, error) {
	if db == nil || g == nil || commerce == nil {
		return nil, b.ErrUnavailable
	}
	cfg, e := configured(cfg)
	if e != nil {
		return nil, e
	}
	return &Repository{db, g, commerce, cfg}, nil
}

// ValidateConfig checks the same owned policy as New without database access.
func ValidateConfig(cfg Config) error {
	_, e := configured(cfg)
	return e
}

func configured(cfg Config) (Config, error) {
	if cfg.Policy == nil || cfg.Policy.PerkLevels == nil || !authority.ValidID(cfg.TreasuryAccountID) || !authority.ValidID(cfg.TreasuryWalletID) {
		return Config{}, b.ErrUnavailable
	}
	for i, n := range cfg.Policy.LabelCaps {
		if n < 0 || n > 1000000 || i > 0 && n < cfg.Policy.LabelCaps[i-1] {
			return Config{}, b.ErrInvalid
		}
	}
	if cfg.Policy.LabelCaps[0] != 0 {
		return Config{}, b.ErrInvalid
	}
	policy := *cfg.Policy
	policy.PerkLevels = map[string]int{}
	for id, level := range cfg.Policy.PerkLevels {
		if strings.TrimSpace(id) == "" || len(id) > 256 || level < 1 || level > 3 {
			return Config{}, b.ErrInvalid
		}
		policy.PerkLevels[id] = level
	}
	cfg.Policy = &policy
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.OrderTTL == 0 {
		cfg.OrderTTL = time.Hour
	}
	if cfg.OrderTTL < time.Minute || cfg.OrderTTL > 24*time.Hour {
		return Config{}, b.ErrInvalid
	}
	return cfg, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return b.ErrUnavailable
	}
	var yes bool
	e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.id=$1::uuid AND w.account_id=$2::uuid AND w.realm_id IS NULL AND w.deleted_at IS NULL AND NOT a.disabled)`, r.cfg.TreasuryWalletID, r.cfg.TreasuryAccountID).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes {
		return b.ErrUnavailable
	}
	_, e = r.db.ExecContext(ctx, `SELECT id,fulfilled_at,expires_at FROM owned_realm_boost_purchases LIMIT 0;SELECT id,delta FROM owned_realm_boost_experience LIMIT 0;SELECT id FROM owned_realm_boost_audit LIMIT 0`)
	return e
}
func uid() (string, error) {
	x := make([]byte, 16)
	if _, e := rand.Read(x); e != nil {
		return "", e
	}
	x[6] = x[6]&15 | 64
	x[8] = x[8]&63 | 128
	s := hex.EncodeToString(x)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func mapped(e error) error {
	var p *pgconn.PgError
	if errors.As(e, &p) && (p.Code == "55P03" || p.Code == "40P01" || p.Code == "40001") {
		return b.ErrRetry
	}
	if errors.Is(e, sql.ErrNoRows) {
		return b.ErrNotFound
	}
	return e
}
func audit(ctx context.Context, tx *sql.Tx, actor, realm, order, action string, at time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_realm_boost_audit(actor_id,realm_id,order_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,$5::timestamptz)`, actor, realm, order, action, at)
	return e
}

type realmScope struct{ ID, Owner, Name string }

func observe(ctx context.Context, tx *sql.Tx, ref string) (v realmScope, e error) {
	if ref == "" || len(ref) > 256 {
		return v, b.ErrInvalid
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,name FROM owned_realms WHERE deleted_at IS NULL AND(id::text=$1 OR lower(slug)=lower($1))`, ref).Scan(&v.ID, &v.Owner, &v.Name)
	return v, mapped(e)
}

// NOWAIT avoids checkout's actor/order locks conflicting with canonical realm
// writers' sorted-account-before-realm order. Every failure rolls back debit.
func (r *Repository) lockRealm(ctx context.Context, tx *sql.Tx, actor, ref string, pay bool) (v realmScope, e error) {
	perms := []string{"realms.boosts.manage"}
	if pay {
		perms = append(perms, "orders.pay")
	}
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor, perms...); e != nil {
		return
	}
	before, e := observe(ctx, tx, ref)
	if e != nil {
		return v, e
	}
	set := map[string]bool{actor: true, before.Owner: true, r.cfg.TreasuryAccountID: true}
	ids := []string{}
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE NOWAIT`, id).Scan(&active); e != nil {
			return v, mapped(e)
		}
		if !active {
			return v, b.ErrForbidden
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, perms...); e != nil {
		return
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE NOWAIT`, before.ID).Scan(&held); e != nil {
		return v, mapped(e)
	}
	v, e = observe(ctx, tx, ref)
	if e != nil {
		return
	}
	if v.ID != before.ID || v.Owner != before.Owner {
		return v, b.ErrConflict
	}
	var member bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_members WHERE realm_id=$1::uuid AND account_id=$2::uuid AND joined_at IS NOT NULL AND leave_at IS NULL AND role>=0)`, v.ID, actor).Scan(&member)
	if e != nil {
		return
	}
	if !member {
		return v, b.ErrForbidden
	}
	return
}
func (r *Repository) CreateOrder(ctx context.Context, actor, ref, key string, q b.Request) (out b.OrderResponse, e error) {
	defer func() { e = mapped(e) }()
	if r == nil {
		return out, b.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return out, authority.ErrInvalid
	}
	if len(key) > 128 {
		return out, b.ErrInvalid
	}
	currency, e := b.Normalize(q.Currency)
	if e != nil {
		return out, e
	}
	amount, e := b.Price(q.Shares, currency)
	if e != nil {
		return out, e
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	realm, e := r.lockRealm(ctx, tx, actor, ref, false)
	if e != nil {
		return out, e
	}
	raw, _ := json.Marshal([]any{realm.ID, q.Shares, currency, r.cfg.TreasuryWalletID})
	h := sha256.Sum256(raw)
	fp := hex.EncodeToString(h[:])
	prior, e := r.commerce.RealmBoostRequest(ctx, r.db, tx, actor, key, fp, "")
	if e != nil {
		return out, e
	}
	if prior != "" {
		e = tx.QueryRowContext(ctx, `SELECT order_id::text,shares,currency,amount::text FROM owned_realm_boost_purchases WHERE id=$1::uuid AND account_id=$2::uuid AND realm_id=$3::uuid`, prior, actor, realm.ID).Scan(&out.OrderID, &out.Shares, &out.Currency, &out.Amount)
		if e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	id, e := uid()
	if e != nil {
		return out, e
	}
	at := r.cfg.Now().UTC().Truncate(time.Microsecond)
	o, e := r.commerce.CreateRealmBoostOrder(ctx, r.db, tx, actor, id, realm.ID, r.cfg.TreasuryWalletID, currency, q.Shares, amount, at.Add(r.cfg.OrderTTL), at)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_boost_purchases(id,order_id,realm_id,account_id,treasury_account_id,treasury_wallet_id,shares,currency,amount,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::bigint,$8::text,$9::numeric,$10::timestamptz)`, id, o.ID, realm.ID, actor, r.cfg.TreasuryAccountID, r.cfg.TreasuryWalletID, q.Shares, currency, amount, at)
	if e != nil {
		return out, e
	}
	if _, e = r.commerce.RealmBoostRequest(ctx, r.db, tx, actor, key, fp, id); e != nil {
		return out, e
	}
	if e = audit(ctx, tx, actor, realm.ID, o.ID, "created", at); e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "realms.boosts.manage"); e != nil {
		return out, e
	}
	return b.OrderResponse{OrderID: o.ID, Shares: q.Shares, Currency: currency, Amount: amount}, tx.Commit()
}

var _ b.Repository = (*Repository)(nil)
