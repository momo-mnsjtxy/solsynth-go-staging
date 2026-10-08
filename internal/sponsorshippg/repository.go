// SPDX-License-Identifier: AGPL-3.0-only
package sponsorshippg

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
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"time"
)

//go:embed schema.sql
var Schema string

type Commerce interface {
	SponsorRequest(context.Context, *sql.DB, *sql.Tx, string, string, string, string) (string, error)
	CreateSponsorOrder(context.Context, *sql.DB, *sql.Tx, string, string, string, string, wallet.Amount, time.Time, time.Time) (c.Order, error)
}
type Config struct {
	TreasuryAccountID, TreasuryWalletID string
	MaxAmount                           wallet.Amount
	OrderTTL                            time.Duration
	MaxOrdersPerPost                    int
	Now                                 func() time.Time
}
type Repository struct {
	db           *sql.DB
	guard        *authoritypg.TransactionGuard
	commerce     Commerce
	visibility   postpg.VisibilityProvider
	associations postpg.AssociationStore
	publishers   *accountpg.Repository
	cfg          Config
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, commerce Commerce, v postpg.VisibilityProvider, a postpg.AssociationStore, cfg Config) (*Repository, error) {
	if db == nil || g == nil || commerce == nil || v == nil || !authority.ValidID(cfg.TreasuryAccountID) || !authority.ValidID(cfg.TreasuryWalletID) {
		return nil, s.ErrUnavailable
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxAmount.Sign() == 0 {
		cfg.MaxAmount, _ = wallet.ParseAmount("1000000")
	}
	if cfg.MaxAmount.Cmp(s.Minimum()) < 0 {
		return nil, s.ErrInvalid
	}
	if cfg.OrderTTL == 0 {
		cfg.OrderTTL = time.Hour
	}
	if cfg.OrderTTL < time.Minute || cfg.OrderTTL > 24*time.Hour {
		return nil, s.ErrInvalid
	}
	if cfg.MaxOrdersPerPost == 0 {
		cfg.MaxOrdersPerPost = 10000
	}
	if cfg.MaxOrdersPerPost < 1 || cfg.MaxOrdersPerPost > 100000 {
		return nil, s.ErrInvalid
	}
	realms, _ := v.(accountpg.RealmProvider)
	return &Repository{db, g, commerce, v, a, accountpg.New(db).WithRealms(realms), cfg}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return s.ErrUnavailable
	}
	var ok bool
	e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.id=$1::uuid AND w.account_id=$2::uuid AND w.realm_id IS NULL AND w.deleted_at IS NULL AND NOT a.disabled)`, r.cfg.TreasuryWalletID, r.cfg.TreasuryAccountID).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return s.ErrUnavailable
	}
	_, e = r.db.ExecContext(ctx, `SELECT id,fulfilled_at FROM owned_post_sponsorships LIMIT 0;SELECT id FROM owned_post_sponsor_placements LIMIT 0;SELECT post_id FROM owned_post_sponsor_impressions LIMIT 0;SELECT id FROM owned_post_sponsor_audit LIMIT 0`)
	return e
}
func uid() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	x := hex.EncodeToString(b)
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:], nil
}
func mapped(e error) error {
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "55P03" {
		return s.ErrRetry
	}
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, posts.ErrNotFound) || errors.Is(e, accounts.ErrNotFound) {
		return s.ErrNotFound
	}
	return e
}
func audit(ctx context.Context, tx *sql.Tx, actor, post, order, action string, at time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_post_sponsor_audit(actor_id,post_id,order_id,action,occurred_at)VALUES(NULLIF($1::text,'')::uuid,$2::uuid,NULLIF($3::text,'')::uuid,$4::text,$5::timestamptz)`, actor, post, order, action, at)
	return e
}

type scope struct{ Publisher, Owner, Realm, RealmOwner string }

func observe(ctx context.Context, tx *sql.Tx, id string) (o scope, e error) {
	if !authority.ValidID(id) {
		return o, s.ErrNotFound
	}
	e = tx.QueryRowContext(ctx, `SELECT u.id::text,COALESCE(u.account_id::text,''),COALESCE(u.realm_id::text,''),COALESCE(r.account_id::text,'') FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id LEFT JOIN owned_realms r ON r.id=u.realm_id WHERE p.id=$1::uuid`, id).Scan(&o.Publisher, &o.Owner, &o.Realm, &o.RealmOwner)
	return o, mapped(e)
}

// Checkout already owns actor/order. Secondary locks are NOWAIT so an opposite
// account/domain lock order rolls back before a debit instead of deadlocking.
func (r *Repository) lockPost(ctx context.Context, tx *sql.Tx, actor, id string, pay bool) (posts.Post, error) {
	var out posts.Post
	perms := []string{"posts.sponsor"}
	if pay {
		perms = append(perms, "orders.pay")
	}
	if _, e := r.guard.Preflight(ctx, r.db, tx, actor, perms...); e != nil {
		return out, e
	}
	before, e := observe(ctx, tx, id)
	if e != nil {
		return out, e
	}
	set := map[string]bool{actor: true, r.cfg.TreasuryAccountID: true}
	for _, v := range []string{before.Owner, before.RealmOwner} {
		if v != "" {
			set[v] = true
		}
	}
	ids := []string{}
	for v := range set {
		ids = append(ids, v)
	}
	sort.Strings(ids)
	for _, v := range ids {
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE NOWAIT`, v).Scan(&active); e != nil {
			return out, mapped(e)
		}
		if !active {
			return out, s.ErrForbidden
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, perms...); e != nil {
		return out, e
	}
	var held string
	if before.Realm != "" {
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, before.Realm).Scan(&held); e != nil {
			return out, mapped(e)
		}
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, before.Publisher).Scan(&held); e != nil {
		return out, mapped(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE NOWAIT`, id).Scan(&held); e != nil {
		return out, mapped(e)
	}
	after, e := observe(ctx, tx, id)
	if e != nil {
		return out, e
	}
	if before != after {
		return out, s.ErrConflict
	}
	return r.eligible(ctx, tx, actor, id)
}
func (r *Repository) eligibleSQL() string {
	return ` AND p.visibility=0 AND p.realm_id IS NULL AND p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND p.shadowbanned_at IS NULL AND u.shadowbanned_at IS NULL AND ` + postpg.DetailPredicate(r.visibility) + ` AND ` + r.publicPublisherSQL()
}
func (r *Repository) publicPublisherSQL() string {
	v, ok := r.visibility.(accountpg.RealmProvider)
	if !ok {
		return `u.type=0`
	}
	return v.PublisherVisibilitySQL("u", "''")
}
func (r *Repository) eligible(ctx context.Context, tx *sql.Tx, actor, id string) (posts.Post, error) {
	if !authority.ValidID(id) {
		return posts.Post{}, s.ErrNotFound
	}
	if actor != "" && !authority.ValidID(actor) {
		return posts.Post{}, authority.ErrInvalid
	}
	p, e := postpg.ScanProjection(tx.QueryRowContext(ctx, `SELECT `+postpg.ProjectionColumns+postpg.ActivePostFrom+r.eligibleSQL()+` AND p.id=$2::uuid`, actor, id))
	return p, mapped(e)
}
func (r *Repository) CreateOrder(ctx context.Context, actor, post, key string, q s.Request) (out s.OrderResponse, e error) {
	defer func() { e = mapped(e) }()
	if r == nil {
		return out, s.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return out, authority.ErrInvalid
	}
	if q.Amount.Cmp(s.Minimum()) < 0 || q.Amount.Cmp(r.cfg.MaxAmount) > 0 || len(key) > 128 {
		return out, s.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	p, e := r.lockPost(ctx, tx, actor, post, false)
	if e != nil {
		return out, e
	}
	b, _ := json.Marshal(struct {
		Post, Treasury string
		Amount         wallet.Amount
	}{post, r.cfg.TreasuryWalletID, q.Amount})
	h := sha256.Sum256(b)
	fp := hex.EncodeToString(h[:])
	prior, e := r.commerce.SponsorRequest(ctx, r.db, tx, actor, key, fp, "")
	if e != nil {
		return out, e
	}
	if prior != "" {
		e = tx.QueryRowContext(ctx, `SELECT order_id::text,amount::text FROM owned_post_sponsorships WHERE id=$1::uuid AND account_id=$2::uuid AND post_id=$3::uuid`, prior, actor, post).Scan(&out.OrderID, &out.Amount)
		if e != nil {
			return out, mapped(e)
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "posts.sponsor"); e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_post_sponsorships WHERE post_id=$1::uuid`, post).Scan(&count); e != nil {
		return out, e
	}
	if count >= r.cfg.MaxOrdersPerPost {
		return out, s.ErrConflict
	}
	id, e := uid()
	if e != nil {
		return out, e
	}
	at := r.cfg.Now().UTC().Truncate(time.Microsecond)
	o, e := r.commerce.CreateSponsorOrder(ctx, r.db, tx, actor, id, post, r.cfg.TreasuryWalletID, q.Amount, at.Add(r.cfg.OrderTTL), at)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_sponsorships(id,order_id,post_id,account_id,publisher_id,treasury_account_id,treasury_wallet_id,currency,amount,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,'golds',$8::numeric,$9::timestamptz)`, id, o.ID, post, actor, p.PublisherID, r.cfg.TreasuryAccountID, r.cfg.TreasuryWalletID, q.Amount, at)
	if e != nil {
		return out, e
	}
	if _, e = r.commerce.SponsorRequest(ctx, r.db, tx, actor, key, fp, id); e != nil {
		return out, e
	}
	if e = audit(ctx, tx, actor, post, o.ID, "created", at); e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "posts.sponsor"); e != nil {
		return out, e
	}
	return s.OrderResponse{OrderID: o.ID, Amount: q.Amount}, tx.Commit()
}
