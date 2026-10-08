// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db         *sql.DB
	publishers *accountpg.Repository
	now        func() time.Time
	guard      *authoritypg.TransactionGuard
	visibility postpg.VisibilityProvider
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db, publishers: accountpg.New(db), now: time.Now}
}

var _ interactions.Repository = (*Repository)(nil)
var _ postpg.AssociationStore = (*Repository)(nil)

func newID() (string, error) {
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
		return interactions.ErrNotFound
	}
	return e
}
func conflict(e error) bool { var p *pgconn.PgError; return errors.As(e, &p) && p.Code == "23505" }
func (r *Repository) write(ctx context.Context, actor, permission string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil {
		return interactions.ErrUnavailable
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
		if permission == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
		}
		if e != nil {
			return e
		}
	}
	var disabled bool
	if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&disabled); e != nil {
		return missing(e)
	}
	if disabled {
		return posts.ErrForbidden
	}
	if e = fn(tx, r.now().UTC()); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) read(ctx context.Context, actor string, required bool) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, interactions.ErrUnavailable
	}
	if actor == "" && required || actor != "" && !authority.ValidID(actor) {
		return nil, posts.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		var enabled bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&enabled)
		if e != nil || !enabled {
			tx.Rollback()
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			return nil, posts.ErrUnauthenticated
		}
	}
	return tx, nil
}
func audit(ctx context.Context, tx *sql.Tx, actor, post, collection, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_post_interaction_audit(actor_id,post_id,collection_id,action,occurred_at)VALUES($1,NULLIF($2,'')::uuid,NULLIF($3,'')::uuid,$4,$5)`, actor, post, collection, action, now)
	return e
}
func (r *Repository) lockPost(ctx context.Context, tx *sql.Tx, actor, id string) (posts.Post, error) {
	if !authority.ValidID(id) {
		return posts.Post{}, posts.ErrNotFound
	}
	var pub string
	if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&pub); e != nil {
		return posts.Post{}, missing(e)
	}
	scopes, e := postpg.PrelockPublisherRealms(ctx, tx, r.visibility, []string{pub})
	if e != nil {
		return posts.Post{}, e
	}
	var held string
	if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, pub).Scan(&held); e != nil {
		return posts.Post{}, missing(e)
	}
	if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&held); e != nil {
		return posts.Post{}, missing(e)
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return posts.Post{}, e
	}
	p, e := postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility)
	if e != nil {
		return p, e
	}
	if p.PublisherID != pub {
		return posts.Post{}, posts.ErrConflict
	}
	if p.DraftedAt != nil {
		return posts.Post{}, posts.ErrNotFound
	}
	if p.LockedAt != nil {
		return posts.Post{}, posts.ErrLocked
	}
	return p, nil
}
func bookmarkScan(s interface{ Scan(...any) error }) (interactions.Bookmark, error) {
	var b interactions.Bookmark
	e := s.Scan(&b.ID, &b.PostID, &b.AccountID, &b.CreatedAt)
	return b, e
}

const bookmarkColumns = `id::text,post_id::text,account_id::text,created_at`

func (r *Repository) Bookmark(ctx context.Context, actor, id string) (interactions.Bookmark, error) {
	var out interactions.Bookmark
	e := r.write(ctx, actor, "posts.bookmark", func(tx *sql.Tx, now time.Time) error {
		if _, e := r.lockPost(ctx, tx, actor, id); e != nil {
			return e
		}
		b, e := bookmarkScan(tx.QueryRowContext(ctx, `SELECT `+bookmarkColumns+` FROM owned_post_bookmarks WHERE account_id=$1 AND post_id=$2`, actor, id))
		if e == nil {
			out = b
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		uid, e := newID()
		if e != nil {
			return e
		}
		out = interactions.Bookmark{ID: uid, PostID: id, AccountID: actor, CreatedAt: now}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_bookmarks(id,post_id,account_id,created_at)VALUES($1,$2,$3,$4)`, uid, id, actor, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, id, "", "bookmark", now)
	})
	if e != nil {
		return interactions.Bookmark{}, e
	}
	return out, nil
}
func (r *Repository) Unbookmark(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return interactions.ErrNotFound
	}
	return r.write(ctx, actor, "posts.bookmark", func(tx *sql.Tx, now time.Time) error {
		res, e := tx.ExecContext(ctx, `DELETE FROM owned_post_bookmarks WHERE account_id=$1 AND post_id=$2`, actor, id)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return interactions.ErrNotFound
		}
		return audit(ctx, tx, actor, id, "", "unbookmark", now)
	})
}
func (r *Repository) BookmarkStatus(ctx context.Context, actor, id string) (*interactions.Bookmark, error) {
	if !authority.ValidID(id) {
		return nil, interactions.ErrNotFound
	}
	tx, e := r.read(ctx, actor, true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	// Actor-private saved state remains removable when a post becomes unavailable.
	b, e := bookmarkScan(tx.QueryRowContext(ctx, `SELECT `+bookmarkColumns+` FROM owned_post_bookmarks WHERE account_id=$1 AND post_id=$2`, actor, id))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return &b, nil
}
func (r *Repository) React(ctx context.Context, actor, id string, v interactions.ReactionRequest) (interactions.Reaction, bool, error) {
	var out interactions.Reaction
	removed := false
	if e := interactions.ValidateReaction(v); e != nil {
		return out, false, e
	}
	e := r.write(ctx, actor, "posts.react", func(tx *sql.Tx, now time.Time) error {
		if _, e := r.lockPost(ctx, tx, actor, id); e != nil {
			return e
		}
		res, e := tx.ExecContext(ctx, `DELETE FROM owned_post_reactions WHERE account_id=$1 AND post_id=$2 AND symbol=$3`, actor, id, v.Symbol)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 0 {
			removed = true
			return audit(ctx, tx, actor, id, "", "unreact", now)
		}
		uid, e := newID()
		if e != nil {
			return e
		}
		out = interactions.Reaction{ID: uid, PostID: id, AccountID: actor, Symbol: v.Symbol, Attitude: v.Attitude, CreatedAt: now}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_reactions(id,post_id,account_id,symbol,attitude,created_at)VALUES($1,$2,$3,$4,$5,$6)`, uid, id, actor, v.Symbol, v.Attitude, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, id, "", "react", now)
	})
	if e != nil {
		return interactions.Reaction{}, false, e
	}
	return out, removed, nil
}
func (r *Repository) Reactions(ctx context.Context, actor, id string, p interactions.Page) ([]interactions.Reaction, int64, error) {
	if !interactions.ValidPage(p) {
		return nil, 0, interactions.ErrInvalid
	}
	if !authority.ValidID(id) {
		return nil, 0, posts.ErrNotFound
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if _, e = postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility); e != nil {
		return nil, 0, e
	}
	where := ` FROM owned_post_reactions r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.post_id=$1 AND ($2='' OR r.symbol=$2) AND NOT a.disabled`
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, id, p.Symbol).Scan(&total); e != nil {
		return nil, 0, e
	}
	order := `r.symbol,r.created_at DESC,r.id DESC`
	if p.Order == "created" {
		order = `r.created_at DESC,r.id DESC`
	}
	// Pinned source uses Take(take).Skip(offset), not conventional pagination.
	rows, e := tx.QueryContext(ctx, `SELECT r.id::text,r.post_id::text,r.account_id::text,r.symbol,r.attitude,r.created_at,a.name,a.nick,a.language,a.region,a.created_at`+where+` ORDER BY `+order+` OFFSET $3 LIMIT $4`, id, p.Symbol, p.Offset, max(0, p.Take-p.Offset))
	if e != nil {
		return nil, 0, e
	}
	out := []interactions.Reaction{}
	for rows.Next() {
		var v interactions.Reaction
		v.Account = &accounts.Account{}
		e = rows.Scan(&v.ID, &v.PostID, &v.AccountID, &v.Symbol, &v.Attitude, &v.CreatedAt, &v.Account.Name, &v.Account.Nick, &v.Account.Language, &v.Account.Region, &v.Account.CreatedAt)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		v.Account.ID = v.AccountID
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}
func (r *Repository) listPosts(ctx context.Context, tx *sql.Tx, actor, where, order string, args []any, p interactions.Page) ([]posts.Post, int64, error) {
	var total int64
	if e := tx.QueryRowContext(ctx, `SELECT count(*)`+postpg.ActivePostFrom+` AND `+postpg.ListingPredicate(r.visibility)+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	// All callers supply one fixed second selector parameter; no SQL comes from HTTP.
	rows, e := tx.QueryContext(ctx, `SELECT `+postpg.ProjectionColumns+postpg.ActivePostFrom+` AND `+postpg.ListingPredicate(r.visibility)+where+` ORDER BY `+order+` OFFSET $3 LIMIT $4`, append(args, p.Offset, p.Take)...)
	if e != nil {
		return nil, 0, e
	}
	out := []posts.Post{}
	for rows.Next() {
		v, e := postpg.ScanProjection(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		if e = r.Load(ctx, tx, actor, &out[i]); e != nil {
			return nil, 0, e
		}
	}
	return out, total, nil
}
func (r *Repository) Bookmarks(ctx context.Context, actor string, p interactions.Page) ([]posts.Post, int64, error) {
	if !interactions.ValidPage(p) {
		return nil, 0, interactions.ErrInvalid
	}
	tx, e := r.read(ctx, actor, true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	out, total, e := r.listPosts(ctx, tx, actor, ` AND EXISTS(SELECT 1 FROM owned_post_bookmarks b WHERE b.post_id=p.id AND b.account_id=$2)`, `(SELECT created_at FROM owned_post_bookmarks b WHERE b.post_id=p.id AND b.account_id=$2) DESC,p.id DESC`, []any{actor, actor}, p)
	if e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}

// NewWithAuthority is the host constructor. A missing guard remains closed.
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
	realms, _ := v.(accountpg.RealmProvider)
	r.publishers.WithRealms(realms)
	return r
}
