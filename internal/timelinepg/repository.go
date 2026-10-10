// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"time"
)

//go:embed schema.sql
var baseSchema string

//go:embed subscription_state_schema.sql
var SubscriptionStateSchema string

var Schema = baseSchema + "\n" + SubscriptionStateSchema

type Repository struct {
	db                        *sql.DB
	guard                     *authoritypg.TransactionGuard
	visibility                postpg.VisibilityProvider
	associations              postpg.AssociationStore
	now                       func() time.Time
	automod                   bool
	subscriptionNotifications SubscriptionNotifications
	subscriptionStreams       SubscriptionStreams
	automodReview             bool
}

// WithAutomod enables source-observed ranked-feed effects; Latest bypasses them.
func (r *Repository) WithAutomod(review bool) *Repository {
	r.automod = true
	r.automodReview = review
	return r
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db, now: time.Now, visibility: socialpg.New(db)}
}
func NewWithAuthority(db *sql.DB, g *authoritypg.TransactionGuard) *Repository {
	r := New(db)
	if g == nil {
		g = authoritypg.NewTransactionGuard(nil, nil)
	}
	r.guard = g
	return r
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	if r.guard == nil {
		return ctx
	}
	return r.guard.Bind(ctx, p)
}
func (r *Repository) WithVisibility(v postpg.VisibilityProvider) *Repository {
	r.visibility = v
	return r
}
func (r *Repository) WithAssociations(a postpg.AssociationStore) *Repository {
	r.associations = a
	return r
}

var _ timeline.Repository = (*Repository)(nil)
var _ postpg.AssociationStore = (*Repository)(nil)

func id() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func missing(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return posts.ErrNotFound
	}
	return e
}
func (r *Repository) read(ctx context.Context, actor string, required bool) (*sql.Tx, error) {
	if r == nil || r.db == nil || r.visibility == nil {
		return nil, posts.ErrUnavailable
	}
	if actor == "" && required || actor != "" && !authority.ValidID(actor) {
		return nil, posts.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		var active bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&active)
		if e != nil || !active {
			tx.Rollback()
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			return nil, posts.ErrUnauthenticated
		}
	}
	return tx, nil
}
func (r *Repository) write(ctx context.Context, actor, permission string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return posts.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if r.guard != nil {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
			return e
		}
	}
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&active); e != nil {
		return missing(e)
	}
	if !active {
		return posts.ErrUnauthenticated
	}
	if e = fn(tx, r.now().UTC()); e != nil {
		return e
	}
	return tx.Commit()
}
func audit(ctx context.Context, tx *sql.Tx, actor, action, target string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_timeline_audit(actor_id,action,target_id,occurred_at)VALUES($1,$2,NULLIF($3,'')::uuid,$4)`, actor, action, target, now)
	return e
}
func (r *Repository) post(ctx context.Context, tx *sql.Tx, actor, id string) (posts.Post, error) {
	p, e := postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility)
	if e == nil {
		e = r.Load(ctx, tx, actor, &p)
	}
	return p, e
}
func (r *Repository) base() string {
	return postpg.ActivePostFrom + ` AND ` + postpg.ListingPredicate(r.visibility)
}
func (r *Repository) publisher(ctx context.Context, tx *sql.Tx, actor, selector string, lock bool) (string, bool, error) {
	if len(selector) == 0 || len(selector) > 256 {
		return "", false, timeline.ErrPublisherNotFound
	}
	match := accountpg.PublisherNamePredicate("u", "$2")
	if authority.ValidID(selector) {
		match = `u.id=$2::uuid`
	}
	q := `SELECT u.id::text,COALESCE((SELECT requires_approval AND(expires_at IS NULL OR expires_at>statement_timestamp()) FROM owned_publisher_follow_policy f WHERE f.publisher_id=u.id),false) FROM owned_publishers u WHERE u.deleted_at IS NULL AND ` + match + ` AND ` + accountpg.ActivePublisherOwnerPredicate("u") + ` AND $1::text IS NOT NULL AND ` + postpg.PublisherPredicate(r.visibility)
	if r.visibility != nil {
		q += ` AND NOT (` + r.visibility.PostBlockedSQL() + `)`
	}
	var pid string
	var approval bool
	e := tx.QueryRowContext(ctx, q, actor, selector).Scan(&pid, &approval)
	if errors.Is(e, sql.ErrNoRows) {
		return "", false, timeline.ErrPublisherNotFound
	}
	if e != nil || !lock {
		return pid, approval, e
	}
	scopes, e := postpg.PrelockPublisherRealms(ctx, tx, r.visibility, []string{pid})
	if e != nil {
		return "", false, e
	}
	observed := pid
	e = tx.QueryRowContext(ctx, q+` FOR UPDATE OF u`, actor, selector).Scan(&pid, &approval)
	if errors.Is(e, sql.ErrNoRows) {
		return "", false, timeline.ErrPublisherNotFound
	}
	if e != nil {
		return "", false, e
	}
	if pid != observed {
		return "", false, posts.ErrConflict
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return "", false, e
	}
	// The locking SELECT may have waited; realm membership/privacy must be
	// evaluated again in a fresh ReadCommitted statement after that wait.
	e = tx.QueryRowContext(ctx, q, actor, selector).Scan(&pid, &approval)
	if errors.Is(e, sql.ErrNoRows) {
		return "", false, timeline.ErrPublisherNotFound
	}
	return pid, approval, e
}
func (r *Repository) requireManager(ctx context.Context, tx *sql.Tx, actor, publisher string) error {
	realms, _ := r.visibility.(accountpg.RealmProvider)
	if _, e := accountpg.New(nil).WithRealms(realms).RequirePostEditor(ctx, tx, actor, publisher); e != nil {
		return e
	}
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND role>=100 AND joined_at IS NOT NULL AND revoked_at IS NULL)`, publisher, actor).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return posts.ErrForbidden
	}
	return nil
}

func isNotFound(e error) bool { var f *posts.Failure; return errors.As(e, &f) && f.Status == 404 }
