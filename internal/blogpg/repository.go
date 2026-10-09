// SPDX-License-Identifier: AGPL-3.0-only
package blogpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/reader"
	"time"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Verifier  blogs.Verifier
	Previewer blogs.Previewer
	Now       func() time.Time
}
type Repository struct {
	db       *sql.DB
	guard    *authoritypg.TransactionGuard
	accounts *accountpg.Repository
	config   Config
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, c Config) (*Repository, error) {
	if db == nil || g == nil {
		return nil, blogs.ErrUnavailable
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Repository{db, g, accountpg.New(db).WithRealms(socialpg.New(db)), c}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.guard == nil {
		return blogs.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,revision,verified_name FROM owned_publisher_domains LIMIT 0;SELECT domain_id FROM owned_publisher_domain_audit LIMIT 0;SELECT domain_id,revision,expires_at,policy_stamp FROM owned_publisher_domain_intents LIMIT 0;SELECT account_id,revision FROM owned_publisher_blog_authority_epochs LIMIT 0`)
	if e != nil {
		return e
	}
	var fenced bool
	e = r.db.QueryRowContext(ctx, `SELECT count(*)=11 FROM pg_trigger WHERE NOT tgisinternal AND tgenabled IN('O','A') AND tgname IN('owned_publisher_domains_revoke_identity','owned_publisher_domains_revoke_realm_owner','owned_publisher_blog_grants','owned_publisher_blog_punishments','owned_publisher_blog_group_members','owned_publisher_blog_group_grants','owned_publisher_blog_members','owned_publisher_blog_realm_members','owned_publisher_blog_account','owned_publisher_blog_session','owned_publisher_blog_session_delete')`).Scan(&fenced)
	if e != nil {
		return e
	}
	if !fenced {
		return blogs.ErrUnavailable
	}
	return nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return blogs.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return blogs.ErrConflict
	}
	return e
}
func newID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

const columns = `id::text,publisher_id::text,domain,status,verified_at,last_checked_at,failed_attempts,last_error,created_at,updated_at,deleted_at,revision,verified_name`

func scan(s interface{ Scan(...any) error }) (v blogs.Domain, e error) {
	e = s.Scan(&v.ID, &v.PublisherID, &v.Domain, &v.Status, &v.VerifiedAt, &v.LastCheckedAt, &v.FailedAttempts, &v.LastError, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt, &v.Revision, &v.VerifiedName)
	return v, mapped(e)
}
func (r *Repository) begin(ctx context.Context, actor, selector string, manager bool) (*sql.Tx, accounts.Publisher, error) {
	if r == nil || r.db == nil || r.guard == nil {
		return nil, accounts.Publisher{}, blogs.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, accounts.Publisher{}, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, accounts.Publisher{}, e
	}
	if manager {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, "publishers.domains.manage")
	} else {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	}
	if e != nil {
		tx.Rollback()
		return nil, accounts.Publisher{}, e
	}
	p, e := r.accounts.ResolveForPost(ctx, tx, actor, selector)
	if e == nil && manager {
		var ok bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND role>=$3 AND joined_at IS NOT NULL AND revoked_at IS NULL)`, p.ID, actor, accounts.Manager).Scan(&ok)
		if e == nil && !ok {
			e = authority.ErrForbidden
		}
	}
	if e != nil {
		tx.Rollback()
		return nil, accounts.Publisher{}, e
	}
	return tx, p, nil
}
func (r *Repository) finish(ctx context.Context, tx *sql.Tx, actor string, manager bool) error {
	var e error
	if manager {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, "publishers.domains.manage")
	} else {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	}
	if e != nil {
		return e
	}
	return mapped(tx.Commit())
}
func audit(ctx context.Context, tx *sql.Tx, actor string, v blogs.Domain, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_publisher_domain_audit(actor_id,domain_id,action,revision,occurred_at)VALUES($1,$2,$3,$4,$5)`, actor, v.ID, action, v.Revision, now)
	return e
}
func (r *Repository) List(ctx context.Context, actor, selector string) ([]blogs.Domain, error) {
	tx, p, e := r.begin(ctx, actor, selector, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+columns+` FROM owned_publisher_domains WHERE publisher_id=$1 AND deleted_at IS NULL ORDER BY domain,id`, p.ID)
	if e != nil {
		return nil, e
	}
	out := []blogs.Domain{}
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	return out, r.finish(ctx, tx, actor, false)
}
func (r *Repository) Add(ctx context.Context, actor, selector, raw string) (blogs.Domain, error) {
	domain, e := blogs.NormalizeDomain(raw)
	if e != nil {
		return blogs.Domain{}, e
	}
	if r == nil || r.config.Verifier == nil {
		return blogs.Domain{}, blogs.ErrUnavailable
	}
	tx, p, e := r.begin(ctx, actor, selector, true)
	if e != nil {
		return blogs.Domain{}, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_publisher_domains WHERE publisher_id=$1 AND deleted_at IS NULL`, p.ID).Scan(&count); e != nil {
		return blogs.Domain{}, e
	}
	if count >= 128 {
		return blogs.Domain{}, blogs.ErrInvalid
	}
	id, e := newID()
	if e != nil {
		return blogs.Domain{}, e
	}
	now := r.config.Now().UTC()
	v, e := scan(tx.QueryRowContext(ctx, `INSERT INTO owned_publisher_domains(id,publisher_id,domain,status,created_at,updated_at)VALUES($1,$2,$3,0,$4,$4)RETURNING `+columns, id, p.ID, domain, now))
	if e != nil {
		return blogs.Domain{}, e
	}
	if e = r.captureIntent(ctx, tx, actor, v); e != nil {
		return blogs.Domain{}, e
	}
	if e = audit(ctx, tx, actor, v, "add", now); e != nil {
		return blogs.Domain{}, e
	}
	if e = r.finish(ctx, tx, actor, true); e != nil {
		return blogs.Domain{}, e
	}
	return r.verify(ctx, actor, p, v)
}
func (r *Repository) Recheck(ctx context.Context, actor, selector, id string) (blogs.Domain, error) {
	if !authority.ValidID(id) {
		return blogs.Domain{}, blogs.ErrNotFound
	}
	if r == nil || r.config.Verifier == nil {
		return blogs.Domain{}, blogs.ErrUnavailable
	}
	tx, p, e := r.begin(ctx, actor, selector, true)
	if e != nil {
		return blogs.Domain{}, e
	}
	defer tx.Rollback()
	now := r.config.Now().UTC()
	v, e := scan(tx.QueryRowContext(ctx, `UPDATE owned_publisher_domains SET status=0,revision=revision+1,updated_at=$3 WHERE id=$1 AND publisher_id=$2 AND deleted_at IS NULL RETURNING `+columns, id, p.ID, now))
	if e != nil {
		return blogs.Domain{}, e
	}
	if e = r.captureIntent(ctx, tx, actor, v); e != nil {
		return blogs.Domain{}, e
	}
	if e = audit(ctx, tx, actor, v, "recheck", now); e != nil {
		return blogs.Domain{}, e
	}
	if e = r.finish(ctx, tx, actor, true); e != nil {
		return blogs.Domain{}, e
	}
	return r.verify(ctx, actor, p, v)
}

// Verification runs with no SQL transaction or locks held. A fresh authority,
// membership and revision check fences revocation, delete/re-add and late replies.
func (r *Repository) verify(ctx context.Context, actor string, p accounts.Publisher, v blogs.Domain) (blogs.Domain, error) {
	ok, fetchError := r.config.Verifier.VerifyDomain(ctx, v.Domain, p.Name)
	if ctx.Err() != nil {
		return blogs.Domain{}, ctx.Err()
	}
	tx, current, e := r.begin(ctx, actor, p.ID, true)
	if e != nil {
		return blogs.Domain{}, e
	}
	defer tx.Rollback()
	j, e := r.currentIntent(ctx, tx, actor, v)
	if e != nil {
		return blogs.Domain{}, e
	}
	eligible, e := r.pendingEligible(ctx, tx, j)
	if e != nil {
		return blogs.Domain{}, e
	}
	if !eligible {
		return blogs.Domain{}, authority.ErrForbidden
	}
	out, e := r.applyVerification(ctx, tx, actor, p, current, v, ok, fetchError)
	if e != nil {
		return blogs.Domain{}, e
	}
	eligible, e = r.pendingEligible(ctx, tx, j)
	if e != nil {
		return blogs.Domain{}, e
	}
	if !eligible {
		return blogs.Domain{}, authority.ErrForbidden
	}
	return out, r.finish(ctx, tx, actor, true)
}
func (r *Repository) applyVerification(ctx context.Context, tx *sql.Tx, actor string, p, current accounts.Publisher, v blogs.Domain, ok bool, fetchError error) (blogs.Domain, error) {
	if current.Name != p.Name {
		return blogs.Domain{}, blogs.ErrConflict
	}
	locked, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_publisher_domains WHERE id=$1 AND publisher_id=$2 AND deleted_at IS NULL FOR UPDATE`, v.ID, p.ID))
	if e != nil {
		return blogs.Domain{}, e
	}
	if locked.Revision != v.Revision || locked.Status != blogs.Pending {
		return blogs.Domain{}, blogs.ErrConflict
	}
	now := r.config.Now().UTC()
	status, action := blogs.Failed, "failed"
	var lastError, verifiedName *string
	verifiedAt := locked.VerifiedAt
	failures := locked.FailedAttempts
	if ok && fetchError == nil {
		status, action = blogs.Verified, "verified"
		verifiedAt = &now
		verifiedName = &p.Name
	} else {
		if failures == 2147483647 {
			return blogs.Domain{}, blogs.ErrConflict
		}
		failures++
		s := "Publisher name not found in dyson-domains.txt"
		if fetchError != nil {
			s = "Domain verification could not be completed"
		}
		lastError = &s
	}
	v, e = scan(tx.QueryRowContext(ctx, `UPDATE owned_publisher_domains SET status=$2,verified_at=$3,last_checked_at=$4,failed_attempts=$5,last_error=$6,verified_name=$7,updated_at=$4 WHERE id=$1 RETURNING `+columns, v.ID, status, verifiedAt, now, failures, lastError, verifiedName))
	if e != nil {
		return blogs.Domain{}, e
	}
	if e = audit(ctx, tx, actor, v, action, now); e != nil {
		return blogs.Domain{}, e
	}
	return v, nil
}
func (r *Repository) Remove(ctx context.Context, actor, selector, id string) error {
	if !authority.ValidID(id) {
		return blogs.ErrNotFound
	}
	tx, p, e := r.begin(ctx, actor, selector, true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := r.config.Now().UTC()
	v, e := scan(tx.QueryRowContext(ctx, `UPDATE owned_publisher_domains SET deleted_at=$3,updated_at=$3,status=3,revision=revision+1 WHERE id=$1 AND publisher_id=$2 AND deleted_at IS NULL RETURNING `+columns, id, p.ID, now))
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, v, "delete", now); e != nil {
		return e
	}
	return r.finish(ctx, tx, actor, true)
}
func (r *Repository) Preview(ctx context.Context, raw string) (reader.Link, error) {
	if r == nil || r.config.Previewer == nil {
		return reader.Link{}, blogs.ErrUnavailable
	}
	return r.config.Previewer.Preview(ctx, raw)
}

// RequireDomain joins the canonical post writer after its actor/publisher locks.
// It performs no outbound work and deliberately makes no authority grant.
func (r *Repository) RequireDomain(ctx context.Context, tx *sql.Tx, p accounts.Publisher, raw string) error {
	_, e := r.DomainStamp(ctx, tx, p, raw)
	return e
}

// DomainStamp is tied to the exact verified record incarnation. Scheduled intent
// cannot regain eligibility through delete/re-add or a later recheck.
func (r *Repository) DomainStamp(ctx context.Context, tx *sql.Tx, p accounts.Publisher, raw string) (string, error) {
	if r == nil || r.db == nil || tx == nil {
		return "", blogs.ErrUnavailable
	}
	_, domain, e := blogs.NormalizeURL(raw)
	if e != nil {
		return "", e
	}
	v, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_publisher_domains WHERE publisher_id=$1 AND domain=$2 AND deleted_at IS NULL FOR SHARE`, p.ID, domain))
	if errors.Is(e, blogs.ErrNotFound) {
		return "", blogs.ErrUnverified
	}
	if e != nil {
		return "", e
	}
	if v.Status != blogs.Verified || v.VerifiedName == nil || *v.VerifiedName != p.Name {
		return "", blogs.ErrUnverified
	}
	return fmt.Sprintf("%s:%d", v.ID, v.Revision), nil
}

// PolicyStamp is an observation of the current canonical authorization
// incarnation. It grants no permission; the caller retains its own actor locks.
func (r *Repository) PolicyStamp(ctx context.Context, tx *sql.Tx, actor string, publisher accounts.Publisher) (string, error) {
	if r == nil || r.db == nil || tx == nil {
		return "", blogs.ErrUnavailable
	}
	var stamp string
	e := tx.QueryRowContext(ctx, `SELECT jsonb_build_object('actor',COALESCE((SELECT revision FROM owned_publisher_blog_authority_epochs WHERE account_id=$1::uuid),0),'owner',COALESCE((SELECT revision FROM owned_publisher_blog_authority_epochs WHERE account_id=p.account_id),0),'realm_owner',COALESCE((SELECT e.revision FROM owned_realms r JOIN owned_publisher_blog_authority_epochs e ON e.account_id=r.account_id WHERE r.id=p.realm_id),0))::text FROM owned_publishers p WHERE p.id=$2::uuid`, actor, publisher.ID).Scan(&stamp)
	return stamp, e
}
