// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Now              func() time.Time
	PostAssociations postpg.AssociationStore
}
type Repository struct {
	db       *sql.DB
	guard    *authoritypg.TransactionGuard
	config   Config
	social   *socialpg.Repository
	accounts *accountpg.Repository
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, cfg Config) (*Repository, error) {
	if db == nil || guard == nil {
		return nil, c.ErrInvalid
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := socialpg.New(db)
	return &Repository{db, guard, cfg, s, accountpg.New(db).WithRealms(s)}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	var ready bool
	e := r.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name)IS NOT NULL)FROM(VALUES('owned_content_moderation_audit'),('owned_content_moderation_notifications'),('owned_post_collections'),('owned_publisher_follows'),('owned_realm_post_moderation'),('owned_text_posts'),('owned_publishers'),('owned_realms'))required(name)`).Scan(&ready)
	if e != nil {
		return e
	}
	if !ready {
		return c.ErrUnavailable
	}
	_, e = r.db.ExecContext(ctx, `SELECT shadowban_reason,shadowbanned_at FROM owned_text_posts LIMIT 0;SELECT shadowban_reason,shadowbanned_at FROM owned_publishers LIMIT 0`)
	return e
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return c.ErrConflict
	}
	return e
}
func sorted(ids []string) []string {
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
func (r *Repository) authorize(ctx context.Context, tx *sql.Tx, actor, permission string, global bool) error {
	p, e := r.guard.Authorize(ctx, r.db, tx, actor, permission)
	if e != nil {
		return e
	}
	if global && !p.Account.Superuser {
		return c.ErrForbidden
	}
	return nil
}

// Authority-only preflight precedes domain observations. Canonical accounts
// are locked in sorted order, then the original proof and current global flag
// are revalidated. Domain waiters recheck once more immediately before writes.
func (r *Repository) begin(ctx context.Context, actor, permission string, global bool, plan func(*sql.Tx) ([]string, error)) (*sql.Tx, error) {
	if r == nil || r.db == nil || r.guard == nil {
		return nil, c.ErrUnavailable
	}
	if !authority.ValidID(actor) || permission == "" {
		return nil, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*sql.Tx, error) { tx.Rollback(); return nil, mapped(e) }
	p, e := r.guard.Preflight(ctx, r.db, tx, actor, permission)
	if e != nil {
		return fail(e)
	}
	if global && !p.Account.Superuser {
		return fail(c.ErrForbidden)
	}
	ids := []string{actor}
	if plan != nil {
		extra, e := plan(tx)
		if e != nil {
			return fail(e)
		}
		ids = append(ids, extra...)
	}
	for _, id := range sorted(ids) {
		if !authority.ValidID(id) {
			return fail(c.ErrInvalid)
		}
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&held); e != nil {
			return fail(e)
		}
	}
	if e = r.authorize(ctx, tx, actor, permission, global); e != nil {
		return fail(e)
	}
	return tx, nil
}
func (r *Repository) audit(ctx context.Context, tx *sql.Tx, actor string, kind c.Kind, id, action string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil || len(b) > 32768 {
		return c.ErrInvalid
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_content_moderation_audit(actor_id,kind,target_id,action,detail,occurred_at)VALUES($1,$2,$3,$4,$5::jsonb,$6)`, actor, kind, id, action, b, r.config.Now().UTC())
	return e
}
func selector(alias, column, ref string) string {
	if authority.ValidID(ref) {
		return alias + `.id=$1::uuid`
	}
	return `lower(` + alias + `.` + column + `)=lower($1)`
}
func validRef(s string) bool { return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s }
func (r *Repository) publisher(ctx context.Context, tx *sql.Tx, ref string) (c.Publisher, error) {
	var p c.Publisher
	var verification []byte
	if !validRef(ref) {
		return p, c.ErrInvalid
	}
	e := tx.QueryRowContext(ctx, `SELECT p.id::text,p.type,p.name,p.nick,p.bio,p.account_id::text,p.realm_id::text,p.created_at,p.updated_at,COALESCE(p.shadowban_reason,'none'),p.shadowbanned_at,p.verification FROM owned_publishers p WHERE p.deleted_at IS NULL AND `+publisherSelector(ref), ref).Scan(&p.ID, &p.Type, &p.Name, &p.Nick, &p.Bio, &p.AccountID, &p.RealmID, &p.CreatedAt, &p.UpdatedAt, &p.ShadowbanReason, &p.ShadowbannedAt, &verification)
	if e == nil {
		p.Verification, e = accounts.DecodeVerification(verification)
	}
	return p, mapped(e)
}
func (r *Repository) realm(ctx context.Context, tx *sql.Tx, ref string) (social.Realm, error) {
	var v social.Realm
	var verification []byte
	if !validRef(ref) {
		return v, c.ErrInvalid
	}
	e := tx.QueryRowContext(ctx, `SELECT r.id::text,r.slug,r.name,r.description,r.account_id::text,r.is_public,r.is_community,r.boost_level,r.created_at,r.updated_at,r.verification FROM owned_realms r WHERE r.deleted_at IS NULL AND `+selector("r", "slug", ref), ref).Scan(&v.ID, &v.Slug, &v.Name, &v.Description, &v.AccountID, &v.IsPublic, &v.IsCommunity, &v.BoostLevel, &v.CreatedAt, &v.UpdatedAt, &verification)
	if e == nil {
		v.Verification, e = accounts.DecodeVerification(verification)
	}
	return v, mapped(e)
}
func (r *Repository) post(ctx context.Context, tx *sql.Tx, id string) (c.Post, error) {
	var p c.Post
	v, e := postpg.ReadForModerationInTransaction(ctx, tx, id)
	if e != nil {
		return p, e
	}
	p.Post = v
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(shadowban_reason,'none'),shadowbanned_at FROM owned_text_posts WHERE id=$1`, id).Scan(&p.ShadowbanReason, &p.ShadowbannedAt)
	return p, mapped(e)
}
func (r *Repository) get(ctx context.Context, tx *sql.Tx, kind c.Kind, ref string) (any, error) {
	switch kind {
	case c.PostKind:
		return r.post(ctx, tx, ref)
	case c.PublisherKind:
		p, e := r.publisher(ctx, tx, ref)
		if e != nil {
			return nil, e
		}
		v := c.PublisherDetail{Publisher: p}
		e = tx.QueryRowContext(ctx, `SELECT (SELECT count(*)FROM owned_publisher_members WHERE publisher_id=$1 AND joined_at IS NOT NULL AND revoked_at IS NULL),(SELECT count(*)FROM owned_text_posts WHERE publisher_id=$1 AND deleted_at IS NULL),(SELECT count(*)FROM owned_post_collections WHERE publisher_id=$1 AND deleted_at IS NULL),(SELECT count(*)FROM owned_publisher_follows WHERE publisher_id=$1 AND state='accepted' AND ended_at IS NULL)`, p.ID).Scan(&v.MemberCount, &v.PostCount, &v.CollectionCount, &v.SubscriberCount)
		return v, e
	case c.RealmKind:
		realm, e := r.realm(ctx, tx, ref)
		if e != nil {
			return nil, e
		}
		v := c.RealmDetail{Realm: realm}
		e = tx.QueryRowContext(ctx, `SELECT count(*)FILTER(WHERE joined_at IS NOT NULL),count(*)FILTER(WHERE joined_at IS NULL)FROM owned_realm_members WHERE realm_id=$1 AND leave_at IS NULL`, realm.ID).Scan(&v.MemberCount, &v.PendingInviteCount)
		return v, e
	}
	return nil, c.ErrInvalid
}
func (r *Repository) Get(ctx context.Context, actor string, kind c.Kind, ref string) (any, error) {
	permission := kind.Permission("read")
	tx, e := r.begin(ctx, actor, permission, true, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	v, e := r.get(ctx, tx, kind, ref)
	if e != nil {
		return nil, e
	}
	if e = r.authorize(ctx, tx, actor, permission, true); e != nil {
		return nil, e
	}
	return v, tx.Commit()
}
func activeTarget(ctx context.Context, tx *sql.Tx, id string) error {
	var enabled bool
	e := tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1`, id).Scan(&enabled)
	if e != nil {
		return mapped(e)
	}
	if !enabled {
		return c.ErrForbidden
	}
	return nil
}
func (r *Repository) member(ctx context.Context, tx *sql.Tx, actor, realm, target string) (social.Member, error) {
	var v social.Member
	e := tx.QueryRowContext(ctx, `SELECT id::text,realm_id::text,account_id::text,role,joined_at,leave_at,created_at,updated_at FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2 AND leave_at IS NULL`, realm, target).Scan(&v.ID, &v.RealmID, &v.AccountID, &v.Role, &v.JoinedAt, &v.LeaveAt, &v.CreatedAt, &v.UpdatedAt)
	if e != nil {
		return v, mapped(e)
	}
	a, e := r.accounts.AccountInTransaction(ctx, tx, actor, target)
	if e == nil {
		v.Account = &a
	} else if !errors.Is(e, accounts.ErrNotFound) {
		return v, e
	}
	return v, nil
}

var _ c.Repository = (*Repository)(nil)

func publisherSelector(ref string) string {
	if authority.ValidID(ref) {
		return `p.id=$1::uuid`
	}
	return accountpg.PublisherNamePredicate("p", "$1")
}
