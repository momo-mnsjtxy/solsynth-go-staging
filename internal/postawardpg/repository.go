// SPDX-License-Identifier: AGPL-3.0-only
package postawardpg

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
	accounts "solsynth.local/go-migration/ports/passport/accounts"
	a "solsynth.local/go-migration/ports/sphere/postawards"
	"solsynth.local/go-migration/ports/sphere/posts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"time"
)

//go:embed schema.sql
var Schema string

type Commerce interface {
	PostAwardRequest(context.Context, *sql.DB, *sql.Tx, string, string, string, string) (string, error)
	CreatePostAwardOrder(context.Context, *sql.DB, *sql.Tx, string, string, string, string, wallet.Amount, int, time.Time, time.Time) (c.Order, error)
	ReservePostAwardShare(context.Context, *sql.DB, *sql.Tx, string, string, string, string, c.Order, wallet.Amount, time.Time) (c.Order, error)
	ReleasePostAwardShare(context.Context, *sql.DB, *sql.Tx, string, time.Time) error
}
type Config struct {
	TreasuryAccountID, TreasuryWalletID, Policy string
	MaxAmount                                   wallet.Amount
	OrderTTL                                    time.Duration
	MaxOrdersPerPost                            int
	Now                                         func() time.Time
}
type Repository struct {
	db         *sql.DB
	guard      *authoritypg.TransactionGuard
	commerce   Commerce
	visibility postpg.VisibilityProvider
	publishers *accountpg.Repository
	cfg        Config
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, c Commerce, v postpg.VisibilityProvider, cfg Config) (*Repository, error) {
	if db == nil || g == nil || c == nil || v == nil || !authority.ValidID(cfg.TreasuryAccountID) || !authority.ValidID(cfg.TreasuryWalletID) {
		return nil, a.ErrUnavailable
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Policy == "" {
		cfg.Policy = a.MerchantFull
	}
	if cfg.Policy != a.MerchantFull && cfg.Policy != a.Documented80 {
		return nil, a.ErrInvalid
	}
	if cfg.MaxAmount.Sign() == 0 {
		cfg.MaxAmount, _ = wallet.ParseAmount("1000000")
	}
	max, _ := wallet.ParseAmount("1000000000")
	if cfg.MaxAmount.Sign() < 1 || cfg.MaxAmount.Cmp(max) > 0 {
		return nil, a.ErrInvalid
	}
	if cfg.OrderTTL == 0 {
		cfg.OrderTTL = time.Hour
	}
	if cfg.OrderTTL < time.Minute || cfg.OrderTTL > 24*time.Hour {
		return nil, a.ErrInvalid
	}
	if cfg.MaxOrdersPerPost == 0 {
		cfg.MaxOrdersPerPost = 10000
	}
	if cfg.MaxOrdersPerPost < 1 || cfg.MaxOrdersPerPost > 100000 {
		return nil, a.ErrInvalid
	}
	realms, _ := v.(accountpg.RealmProvider)
	return &Repository{db, g, c, v, accountpg.New(db).WithRealms(realms), cfg}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return a.ErrUnavailable
	}
	var ok bool
	e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts u ON u.id=w.account_id WHERE w.id=$1::uuid AND w.account_id=$2::uuid AND w.realm_id IS NULL AND w.deleted_at IS NULL AND NOT u.disabled)`, r.cfg.TreasuryWalletID, r.cfg.TreasuryAccountID).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return a.ErrUnavailable
	}
	_, e = r.db.ExecContext(ctx, `SELECT purchase_id,next_attempt_at FROM owned_post_award_receipts LIMIT 0;SELECT id,policy,share_amount FROM owned_post_award_purchases LIMIT 0;SELECT awarded_score FROM owned_text_posts LIMIT 0;SELECT publisher_id FROM owned_publisher_payout_wallets LIMIT 0;SELECT id FROM owned_post_award_audit LIMIT 0`)
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
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "55P03" {
		return a.ErrRetry
	}
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, posts.ErrNotFound) || errors.Is(e, accounts.ErrNotFound) {
		return a.ErrNotFound
	}
	return e
}
func audit(ctx context.Context, tx *sql.Tx, actor, pub, post, order, action string, at time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_post_award_audit(actor_id,publisher_id,post_id,order_id,action,occurred_at)VALUES(NULLIF($1::text,'')::uuid,$2::uuid,NULLIF($3::text,'')::uuid,NULLIF($4::text,'')::uuid,$5::text,$6::timestamptz)`, actor, pub, post, order, action, at)
	return e
}

type scope struct{ Publisher, Owner, Realm, RealmOwner, PostRealm, PostRealmOwner string }

func observe(ctx context.Context, tx *sql.Tx, selector, post string) (s scope, e error) {
	q := `SELECT u.id::text,COALESCE(u.account_id::text,''),COALESCE(u.realm_id::text,''),COALESCE(r.account_id::text,''),''::text,''::text FROM owned_publishers u LEFT JOIN owned_realms r ON r.id=u.realm_id WHERE ` + accountpg.PublisherNamePredicate("u", "$1")
	if post != "" {
		selector = post
		q = `SELECT u.id::text,COALESCE(u.account_id::text,''),COALESCE(u.realm_id::text,''),COALESCE(r.account_id::text,''),COALESCE(p.realm_id::text,''),COALESCE(pr.account_id::text,'') FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id LEFT JOIN owned_realms r ON r.id=u.realm_id LEFT JOIN owned_realms pr ON pr.id=p.realm_id WHERE p.id=$1::uuid`
	}
	e = tx.QueryRowContext(ctx, q, selector).Scan(&s.Publisher, &s.Owner, &s.Realm, &s.RealmOwner, &s.PostRealm, &s.PostRealmOwner)
	return s, mapped(e)
}
func unique(ids ...string) []string {
	set := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			set[id] = true
		}
	}
	out := []string{}
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func (r *Repository) lockScope(ctx context.Context, tx *sql.Tx, actor, selector, post string, permissions ...string) (scope, error) {
	var out scope
	if _, e := r.guard.Preflight(ctx, r.db, tx, actor, permissions...); e != nil {
		return out, e
	}
	before, e := observe(ctx, tx, selector, post)
	if e != nil {
		return out, e
	}
	for _, id := range unique(actor, r.cfg.TreasuryAccountID, before.Owner, before.RealmOwner, before.PostRealmOwner) {
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE NOWAIT`, id).Scan(&active); e != nil {
			return out, mapped(e)
		}
		if !active {
			return out, a.ErrForbidden
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permissions...); e != nil {
		return out, e
	}
	for _, id := range unique(before.Realm, before.PostRealm) {
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, id).Scan(&held); e != nil {
			return out, mapped(e)
		}
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE NOWAIT`, before.Publisher).Scan(&held); e != nil {
		return out, mapped(e)
	}
	if post != "" {
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE NOWAIT`, post).Scan(&held); e != nil {
			return out, mapped(e)
		}
	}
	after, e := observe(ctx, tx, selector, post)
	if e != nil {
		return out, e
	}
	if before != after {
		return out, a.ErrRetry
	}
	if post != "" {
		if _, e = r.visiblePost(ctx, tx, actor, post); e != nil {
			return out, e
		}
	} else {
		if _, e = r.publishers.VisiblePublisherInTransaction(ctx, tx, actor, before.Publisher); e != nil {
			return out, mapped(e)
		}
	}
	return after, nil
}
func (r *Repository) visiblePost(ctx context.Context, tx *sql.Tx, actor, post string) (posts.Post, error) {
	if !authority.ValidID(post) {
		return posts.Post{}, a.ErrNotFound
	}
	p, e := postpg.ScanProjection(tx.QueryRowContext(ctx, `SELECT `+postpg.ProjectionColumns+postpg.ActivePostFrom+` AND `+postpg.DetailPredicate(r.visibility)+` AND p.id=$2::uuid AND p.drafted_at IS NULL AND p.published_at<=$3::timestamptz AND p.shadowbanned_at IS NULL AND u.shadowbanned_at IS NULL`, actor, post, r.cfg.Now()))
	return p, mapped(e)
}
func (r *Repository) commit(ctx context.Context, tx *sql.Tx, actor string, permissions ...string) error {
	if _, e := r.guard.Authorize(ctx, r.db, tx, actor, permissions...); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) CreateOrder(ctx context.Context, actor, post, key string, q a.Request) (out a.OrderResponse, e error) {
	defer func() { e = mapped(e) }()
	if r == nil {
		return out, a.ErrUnavailable
	}
	if !authority.ValidID(actor) || !authority.ValidID(post) || len(key) > 128 {
		return out, a.ErrInvalid
	}
	if e = q.Validate(r.cfg.MaxAmount); e != nil {
		return
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	s, e := r.lockScope(ctx, tx, actor, "", post, "posts.award")
	if e != nil {
		return out, e
	}
	b, _ := json.Marshal(struct {
		Post    string
		Request a.Request
	}{post, q})
	h := sha256.Sum256(b)
	fp := hex.EncodeToString(h[:])
	prior, e := r.commerce.PostAwardRequest(ctx, r.db, tx, actor, key, fp, "")
	if e != nil {
		return out, e
	}
	if prior != "" {
		e = tx.QueryRowContext(ctx, `SELECT order_id::text FROM owned_post_award_purchases WHERE id=$1::uuid AND account_id=$2::uuid AND post_id=$3::uuid`, prior, actor, post).Scan(&out.OrderID)
		if e != nil {
			return out, e
		}
		return out, r.commit(ctx, tx, actor, "posts.award")
	}
	beneficiary, e := r.binding(ctx, tx, s)
	if e != nil {
		return out, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_post_award_purchases WHERE post_id=$1::uuid`, post).Scan(&count); e != nil {
		return out, e
	}
	if count >= r.cfg.MaxOrdersPerPost {
		return out, a.ErrConflict
	}
	id, e := uid()
	if e != nil {
		return out, e
	}
	at := r.cfg.Now().UTC().Truncate(time.Microsecond)
	share, e := a.Share(q.Amount, q.Attitude, r.cfg.Policy)
	if e != nil {
		return out, e
	}
	o, e := r.commerce.CreatePostAwardOrder(ctx, r.db, tx, actor, id, post, r.cfg.TreasuryWalletID, q.Amount, q.Attitude, at.Add(r.cfg.OrderTTL), at)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_award_purchases(id,order_id,post_id,publisher_id,account_id,treasury_account_id,treasury_wallet_id,beneficiary_wallet_id,beneficiary_account_id,beneficiary_realm_id,amount,attitude,message,policy,share_amount,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8::uuid,NULLIF($9::text,'')::uuid,NULLIF($10::text,'')::uuid,$11::numeric,$12::integer,$13::text,$14::text,$15::numeric,$16::timestamptz)`, id, o.ID, post, s.Publisher, actor, r.cfg.TreasuryAccountID, r.cfg.TreasuryWalletID, beneficiary, s.Owner, s.Realm, q.Amount, q.Attitude, q.Message, r.cfg.Policy, share, at)
	if e != nil {
		return out, e
	}
	if _, e = r.commerce.PostAwardRequest(ctx, r.db, tx, actor, key, fp, id); e != nil {
		return out, e
	}
	if e = audit(ctx, tx, actor, s.Publisher, post, o.ID, "created", at); e != nil {
		return out, e
	}
	return a.OrderResponse{OrderID: o.ID}, r.commit(ctx, tx, actor, "posts.award")
}
