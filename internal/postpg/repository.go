// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	blogs         BlogProvider
	db            *sql.DB
	publishers    *accountpg.Repository
	now           func() time.Time
	authority     *authoritypg.TransactionGuard
	associations  AssociationStore
	visibility    VisibilityProvider
	automod       bool
	automodReview bool
	scheduling    bool
	publications  PublicationObserver
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db, publishers: accountpg.New(db), now: time.Now}
}

// NewWithAuthority rechecks the bound signed proof and operation permission
// under the canonical account lock in each write's own SQL transaction.
func NewWithAuthority(db *sql.DB, guard *authoritypg.TransactionGuard) *Repository {
	r := New(db)
	if guard == nil {
		guard = authoritypg.NewTransactionGuard(nil, nil)
	}
	r.authority = guard
	return r
}

var _ posts.Repository = (*Repository)(nil)

type scanner interface{ Scan(...any) error }

func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return posts.ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(e, &pe) && pe.Code == "23505" {
		return posts.ErrConflict
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
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
func optional(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	return &v
}

const columns = `p.id::text,p.publisher_id::text,p.title,p.description,p.slug,p.content,p.visibility,p.type,p.language,p.drafted_at,p.published_at,p.created_at,p.updated_at,p.deleted_at,p.locked_at,p.realm_id::text,p.awarded_score::text,u.id::text,u.name,u.nick,u.bio,u.type,u.account_id::text,u.realm_id::text,u.created_at,u.updated_at,u.verification,p.blog_metadata,p.pin_mode`
const base = ` FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.deleted_at IS NULL AND u.deleted_at IS NULL AND (u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=u.account_id AND NOT owner.disabled))`

// The owned privacy rule is editor-only access for private/draft posts. This is
// deliberately stricter than assuming unresolved upstream membership semantics.
const editor = `EXISTS(SELECT 1 FROM owned_publisher_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.publisher_id=p.publisher_id AND m.account_id=NULLIF($1,'')::uuid AND NOT a.disabled AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=50)`

func scan(s scanner) (posts.Post, error) {
	var p posts.Post
	var verification, blog []byte
	e := s.Scan(&p.ID, &p.PublisherID, &p.Title, &p.Description, &p.Slug, &p.Content, &p.Visibility, &p.Type, &p.Language, &p.DraftedAt, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.LockedAt, &p.RealmID, &p.AwardedScore,
		&p.Publisher.ID, &p.Publisher.Name, &p.Publisher.Nick, &p.Publisher.Bio, &p.Publisher.Type, &p.Publisher.AccountID, &p.Publisher.RealmID, &p.Publisher.CreatedAt, &p.Publisher.UpdatedAt, &verification, &blog, &p.PinMode)
	if e == nil {
		p.Publisher.Verification, e = accounts.DecodeVerification(verification)
	}
	if e == nil {
		e = decodeBlog(&p, blog)
	}
	return p, mapped(e)
}
func (r *Repository) write(ctx context.Context, actor, permission string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil {
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
	if r.authority != nil {
		if _, e = r.authority.Authorize(ctx, r.db, tx, actor, permission); e != nil {
			return e
		}
	}
	var disabled bool
	if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&disabled); e != nil {
		return mapped(e)
	}
	if disabled {
		return posts.ErrForbidden
	}
	if e = fn(tx, r.now().UTC()); e != nil {
		return mapped(e)
	}
	if r.authority != nil {
		if _, e = r.authority.Authorize(ctx, r.db, tx, actor, permission); e != nil {
			return e
		}
	}
	return mapped(tx.Commit())
}
func audit(ctx context.Context, tx *sql.Tx, actor, id, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at) VALUES($1,$2,$3,$4)`, actor, id, action, now)
	return e
}
func apply(p *posts.Post, v posts.Patch) {
	oldRealm := p.RealmID
	if v.ClearRealm {
		p.RealmID = nil
	} else if v.RealmID != nil {
		id := *v.RealmID
		p.RealmID = &id
	}
	if p.PinMode != nil && *p.PinMode == posts.RealmPin && !SameOptionalID(oldRealm, p.RealmID) {
		p.PinMode = nil
	}
	if v.Title != nil {
		p.Title = optional(v.Title)
	}
	if v.Description != nil {
		p.Description = optional(v.Description)
	}
	if v.Slug != nil {
		p.Slug = optional(v.Slug)
	}
	if v.Content != nil {
		p.Content = *v.Content
	}
	if v.Language != nil {
		p.Language = optional(v.Language)
	}
	if v.Type != nil {
		p.Type = *v.Type
	}
	if v.Visibility != nil {
		p.Visibility = *v.Visibility
	}
	if v.DraftedAt != nil {
		d := v.DraftedAt.UTC()
		p.DraftedAt = &d
		p.PublishedAt = nil
	}
	if v.PublishedAt != nil {
		d := v.PublishedAt.UTC()
		p.PublishedAt = &d
		p.DraftedAt = nil
	}
}
func (r *Repository) Create(ctx context.Context, actor, selector string, v posts.Patch) (posts.Post, error) {
	var p posts.Post
	prepared, e := r.prepareBlog(ctx, actor, selector, "", "create", v)
	if e != nil {
		return p, e
	}
	e = r.write(ctx, actor, "posts.create", func(tx *sql.Tx, now time.Time) error {
		if e := r.validatePatch(v, true, now); e != nil {
			return e
		}
		pub, e := r.createPublisher(ctx, tx, actor, selector, v)
		if e != nil {
			return e
		}
		if pub.Type != 0 && (pub.Type != 1 || pub.RealmID == nil) {
			return posts.ErrUnsupported
		}
		if pub.Type == 1 && v.Visibility != nil && (*v.Visibility == posts.Friends || *v.Visibility == posts.CloseFriends) {
			return posts.ErrUnsupported
		}
		if pub.Type == 1 && pub.RealmID != nil {
			key := social.CanPost
			if v.RepliedPostID != nil {
				key = social.CanComment
			}
			if e = RequireRealmAction(ctx, tx, r.visibility, actor, *pub.RealmID, key); e != nil {
				return e
			}
		}
		if e = r.authorizeRealms(ctx, tx, actor, pub.Type, nil, v, false, v.RepliedPostID != nil); e != nil {
			return e
		}
		id, e := newID()
		if e != nil {
			return e
		}
		p = posts.Post{ID: id, PublisherID: pub.ID, Publisher: pub, CreatedAt: now, UpdatedAt: now, PublishedAt: &now}
		apply(&p, v)
		if e = r.applyBlog(ctx, tx, actor, &p, prepared, v.BlogEmbeds); e != nil {
			return e
		}
		blog, e := encodeBlog(p)
		if e != nil {
			return e
		}
		if e = r.prepareSchedule(ctx, tx, actor, &p, v, now); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_text_posts(id,publisher_id,title,description,slug,content,visibility,type,language,drafted_at,published_at,created_at,updated_at,realm_id,blog_metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12,$13,$14::jsonb)`, p.ID, p.PublisherID, p.Title, p.Description, p.Slug, p.Content, p.Visibility, p.Type, p.Language, p.DraftedAt, p.PublishedAt, now, p.RealmID, blog)
		if e != nil {
			return e
		}
		if e = r.applyAssociations(ctx, tx, actor, &p, v, true); e != nil {
			return e
		}
		if e = r.evaluateAutomod(ctx, tx, actor, &p, "create"); e != nil {
			return e
		}
		if e = audit(ctx, tx, actor, p.ID, "create", now); e != nil {
			return e
		}
		if e = r.syncSchedule(ctx, tx, actor, &p, v, "create", now); e != nil {
			return e
		}
		// Durable enqueue can wait; final composition/blog checks must follow it.
		if e = r.recordPublication(ctx, tx, p, false, now); e != nil {
			return e
		}
		if e = r.finishComposition(ctx, tx, actor, &p); e != nil {
			return e
		}
		if p.Type == posts.Blog {
			if e = r.matchBlogPreparation(ctx, tx, actor, p, prepared); e != nil {
				return e
			}
		}
		if isPublished(p, now) {
			source, x := newID()
			if x != nil {
				return x
			}
			if e = r.recordPostProgressionSource(ctx, tx, "post.create_topical", source, actor, "", p, *p.PublishedAt); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return posts.Post{}, e
	}
	posts.RedactAutomodForEditor(&p)
	return p, nil
}
func (r *Repository) locked(ctx context.Context, tx *sql.Tx, actor, id string, v posts.Patch, deleting bool, publishing ...bool) (posts.Post, error) {
	if !authority.ValidID(id) {
		return posts.Post{}, posts.ErrNotFound
	}
	var publisher string
	var realm *string
	if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&publisher, &realm); e != nil {
		return posts.Post{}, mapped(e)
	}
	if e := r.prelock(ctx, tx, actor, MutationScope{PublisherID: publisher, PostID: id, RealmID: realm, Deleting: deleting, Publishing: len(publishing) > 0 && publishing[0]}, v, false); e != nil {
		return posts.Post{}, e
	}
	pub, e := r.publishers.RequirePostEditor(ctx, tx, actor, publisher)
	if e != nil {
		return posts.Post{}, e
	}
	if pub.Type != 0 && (pub.Type != 1 || pub.RealmID == nil) {
		return posts.Post{}, posts.ErrUnsupported
	}
	p, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+base+` AND p.id=$1 FOR UPDATE OF p`, id))
	if e != nil {
		return posts.Post{}, e
	}
	if p.PublisherID != publisher || !SameOptionalID(p.RealmID, realm) {
		return posts.Post{}, posts.ErrConflict
	}
	replying := false
	if _, supported := r.associations.(interface{ SupportsPostRelations() }); supported && !deleting {
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_post_relations WHERE post_id=$1 AND replied_post_id IS NOT NULL)`, p.ID).Scan(&replying); e != nil {
			return posts.Post{}, e
		}
	}
	if !deleting && p.Publisher.Type == 1 && p.Publisher.RealmID != nil {
		key := social.CanPost
		if replying {
			key = social.CanComment
		}
		if e = RequireRealmAction(ctx, tx, r.visibility, actor, *p.Publisher.RealmID, key); e != nil {
			return posts.Post{}, e
		}
	}
	if e = r.authorizeRealms(ctx, tx, actor, p.Publisher.Type, realm, v, deleting, replying); e != nil {
		return posts.Post{}, e
	}
	if v.RealmID != nil && !SameOptionalID(p.RealmID, v.RealmID) {
		var removed bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_post_moderation WHERE post_id=$1 AND realm_id=$2)`, p.ID, *v.RealmID).Scan(&removed); e != nil {
			return posts.Post{}, e
		}
		if removed {
			return posts.Post{}, posts.ErrConflict
		}
	}
	if p.LockedAt != nil {
		return posts.Post{}, posts.ErrLocked
	}
	posts.RedactAutomodForEditor(&p)
	return p, nil
}
func save(ctx context.Context, tx *sql.Tx, p posts.Post) error {
	blog, e := encodeBlog(p)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_text_posts SET title=$2,description=$3,slug=$4,content=$5,visibility=$6,type=$7,language=$8,drafted_at=$9,published_at=$10,updated_at=$11,deleted_at=$12,pin_mode=CASE WHEN pin_mode=1 AND realm_id IS DISTINCT FROM $13::uuid THEN NULL ELSE pin_mode END,realm_id=$13,blog_metadata=$14::jsonb WHERE id=$1`, p.ID, p.Title, p.Description, p.Slug, p.Content, p.Visibility, p.Type, p.Language, p.DraftedAt, p.PublishedAt, p.UpdatedAt, p.DeletedAt, p.RealmID, blog)
	return e
}
func (r *Repository) mutate(ctx context.Context, actor, id, action string, v posts.Patch) (posts.Post, error) {
	var p posts.Post
	prepared, e := r.prepareBlog(ctx, actor, "", id, action, v)
	if e != nil {
		return p, e
	}
	permission := "posts." + action
	if action == "publish" {
		permission = "posts.create"
	}
	e = r.write(ctx, actor, permission, func(tx *sql.Tx, now time.Time) error {
		if action == "update" {
			if e := r.validatePatch(v, false, now); e != nil {
				return e
			}
		}
		var e error
		publishing := r.scheduling && (action == "publish" || v.PublishedAt != nil && !v.PublishedAt.After(now))
		p, e = r.locked(ctx, tx, actor, id, v, action == "delete", publishing)
		if e != nil {
			return e
		}
		wasPublished := isPublished(p, now)
		wasDraft := p.DraftedAt != nil
		switch action {
		case "update":
			apply(&p, v)
			if p.Publisher.Type == 1 && (p.Visibility == posts.Friends || p.Visibility == posts.CloseFriends) {
				return posts.ErrUnsupported
			}
		case "publish":
			if p.DraftedAt == nil {
				return posts.ErrNotDraft
			}
			p.DraftedAt = nil
			p.PublishedAt = &now
		case "delete":
			p.DeletedAt = &now
		default:
			return posts.ErrInvalid
		}
		p.UpdatedAt = now
		if e = r.applyBlog(ctx, tx, actor, &p, prepared, v.BlogEmbeds); e != nil {
			return e
		}
		if e = r.prepareSchedule(ctx, tx, actor, &p, v, now); e != nil {
			return e
		}
		if e = save(ctx, tx, p); e != nil {
			return e
		}
		if e = r.applyAssociations(ctx, tx, actor, &p, v, false); e != nil {
			return e
		}
		if publishing && wasDraft && p.DraftedAt == nil && p.PublishedAt != nil {
			// Explicit publication records fresh intent but must still pass the
			// same current media, related-post and moderation gates as due work.
			check := p
			reason, e := r.preparePublication(ctx, tx, actor, &check)
			if e != nil {
				return e
			}
			if reason == "" {
				if e = r.evaluateAutomod(ctx, tx, actor, &p, action); e != nil {
					return e
				}
				reason, e = r.checkPublication(ctx, tx, actor, &check)
				if e != nil {
					return e
				}
			}
			if reason != "" {
				return posts.ErrForbidden
			}
		}
		if !(publishing && wasDraft && p.DraftedAt == nil && p.PublishedAt != nil) {
			if e = r.evaluateAutomod(ctx, tx, actor, &p, action); e != nil {
				return e
			}
		}
		if e = audit(ctx, tx, actor, id, action, now); e != nil {
			return e
		}
		if e = r.syncSchedule(ctx, tx, actor, &p, v, action, now); e != nil {
			return e
		}
		// Durable enqueue can wait; final composition/blog checks must follow it.
		if e = r.recordPublication(ctx, tx, p, wasPublished, now); e != nil {
			return e
		}
		if e = r.finishComposition(ctx, tx, actor, &p); e != nil {
			return e
		}
		if p.Type == posts.Blog && p.DeletedAt == nil {
			if e = r.matchBlogPreparation(ctx, tx, actor, p, prepared); e != nil {
				return e
			}
		}
		if !wasPublished && isPublished(p, now) {
			source, x := newID()
			if x != nil {
				return x
			}
			if e = r.recordPostProgressionSource(ctx, tx, "post.create_topical", source, actor, "", p, *p.PublishedAt); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return posts.Post{}, e
	}
	posts.RedactAutomodForEditor(&p)
	return p, nil
}
func (r *Repository) Update(ctx context.Context, actor, id string, v posts.Patch) (posts.Post, error) {
	return r.mutate(ctx, actor, id, "update", v)
}
func (r *Repository) Publish(ctx context.Context, actor, id string) (posts.Post, error) {
	return r.mutate(ctx, actor, id, "publish", posts.Patch{})
}
func (r *Repository) Delete(ctx context.Context, actor, id string) error {
	_, e := r.mutate(ctx, actor, id, "delete", posts.Patch{})
	return e
}

func (r *Repository) read(ctx context.Context, actor string) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, posts.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
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
func (r *Repository) detail(ctx context.Context, actor, filter string, args ...any) (posts.Post, error) {
	tx, e := r.read(ctx, actor)
	if e != nil {
		return posts.Post{}, e
	}
	defer tx.Rollback()
	all := append([]any{actor}, args...)
	p, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+base+` AND `+DetailPredicate(r.visibility)+` AND `+filter, all...))
	if e != nil {
		return posts.Post{}, e
	}
	if e = r.loadAutomod(ctx, tx, actor, &p); e != nil {
		return posts.Post{}, e
	}
	if r.associations != nil {
		if e = r.associations.Load(ctx, tx, actor, &p); e != nil {
			return posts.Post{}, e
		}
	}
	if e = r.loadSchedule(ctx, tx, actor, &p); e != nil {
		return posts.Post{}, e
	}
	if e = tx.Commit(); e != nil {
		return posts.Post{}, e
	}
	posts.RedactAutomodForEditor(&p)
	return p, nil
}
func (r *Repository) Get(ctx context.Context, actor, id string) (posts.Post, error) {
	if !authority.ValidID(id) {
		return posts.Post{}, posts.ErrNotFound
	}
	return r.detail(ctx, actor, `p.id=$2`, id)
}
func (r *Repository) GetSlug(ctx context.Context, actor, publisher, slug string) (posts.Post, error) {
	if publisher == "" || len(publisher) > 256 || slug == "" || len(slug) > 4096 {
		return posts.Post{}, posts.ErrNotFound
	}
	return r.detail(ctx, actor, accountpg.PublisherNamePredicate("u", "$2")+` AND lower(p.slug)=lower($3)`, publisher, slug)
}
func (r *Repository) List(ctx context.Context, actor string, o posts.ListOptions) ([]posts.Post, int64, error) {
	if o.Offset < 0 || o.Take < 1 || o.Take > 100 || len(o.Publisher) > 256 || len(o.Realm) > 256 {
		return nil, 0, posts.ErrInvalid
	}
	if o.Type != nil && *o.Type != posts.Moment && *o.Type != posts.Article && (*o.Type != posts.Blog || r.blogs == nil) {
		return nil, 0, posts.ErrUnsupported
	}
	if o.Pinned != nil && (o.Drafts || *o.Pinned && o.Realm == "" && o.Publisher == "") {
		return nil, 0, posts.ErrPinnedScope
	}
	if o.Drafts && actor == "" {
		return nil, 0, posts.ErrUnauthenticated
	}
	tx, e := r.readList(ctx, actor, o.Pinned != nil)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	publisherSelector := o.Publisher
	if o.Publisher != "" {
		var id string
		match := `(u.id::text=$1 OR ` + accountpg.PublisherNamePredicate("u", "$1") + `)`
		if o.Pinned != nil && authority.ValidID(o.Publisher) {
			match = `u.id=$1::uuid`
		}
		e = tx.QueryRowContext(ctx, `SELECT u.id::text FROM owned_publishers u WHERE u.deleted_at IS NULL AND `+match+` AND(u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=u.account_id AND NOT a.disabled))`, o.Publisher).Scan(&id)
		if e != nil {
			return nil, 0, mapped(e)
		}
		visible, e := CanViewPublisher(ctx, tx, actor, id, r.visibility)
		if e != nil {
			return nil, 0, e
		}
		if !visible {
			return nil, 0, posts.ErrNotFound
		}
		if o.Pinned != nil {
			publisherSelector = id
		}
		if o.Drafts {
			var allowed bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND revoked_at IS NULL AND role>=50)`, id, actor).Scan(&allowed)
			if e != nil {
				return nil, 0, e
			}
			if !allowed {
				return nil, 0, posts.ErrForbidden
			}
		}
	}
	publisherFilter := `($2='' OR u.id::text=$2 OR ` + accountpg.PublisherNamePredicate("u", "$2") + `)`
	if o.Pinned != nil {
		publisherFilter = `($2='' OR u.id::text=$2)`
	}
	where := base + ` AND ` + PublisherPredicate(r.visibility) + ` AND ` + RealmPredicate(r.visibility, false) + ` AND ` + publisherFilter + ` AND ($3::integer IS NULL OR p.type=$3)`
	realm, e := ResolveRealmFilter(ctx, tx, r.visibility, actor, o.Realm)
	if e != nil {
		return nil, 0, e
	}
	where += ` AND ($4::text='' OR p.realm_id=NULLIF($4::text,'')::uuid)`
	order := `p.published_at`
	if o.Drafts {
		where += ` AND p.drafted_at IS NOT NULL AND ` + EditorPredicateWithVisibility(r.visibility)
		if r.visibility != nil {
			where += ` AND NOT (` + r.visibility.PostBlockedSQL() + `)`
		}
		order = `p.drafted_at`
	} else {
		where += ` AND ` + ListingPredicate(r.visibility)
	}
	where += pinFilter(o)
	args := []any{actor, publisherSelector, o.Type, realm}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	direction := " DESC"
	if o.Ascending {
		direction = " ASC"
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+columns+where+` ORDER BY `+order+direction+`,p.id`+direction+` OFFSET $5 LIMIT $6`, append(args, o.Offset, o.Take)...)
	if e != nil {
		return nil, 0, e
	}
	out := []posts.Post{}
	for rows.Next() {
		p, e := scan(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if o.Pinned != nil {
		out, e = r.lockPinnedPage(ctx, tx, actor, out, where, args)
		if e != nil {
			return nil, 0, e
		}
		if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&total); e != nil {
			return nil, 0, e
		}
	}
	for i := range out {
		if e = r.loadAutomod(ctx, tx, actor, &out[i]); e != nil {
			return nil, 0, e
		}
	}
	if r.associations != nil {
		for i := range out {
			if e = r.associations.Load(ctx, tx, actor, &out[i]); e != nil {
				return nil, 0, e
			}
		}
	}
	for i := range out {
		if e = r.loadSchedule(ctx, tx, actor, &out[i]); e != nil {
			return nil, 0, e
		}
	}
	if o.Pinned != nil {
		for i := range out {
			if e = RecheckAssociations(ctx, tx, actor, &out[i], r.associations); errors.Is(e, posts.ErrNotFound) {
				// A root withdrawn during hydration is filtered by the final predicate.
				continue
			} else if e != nil {
				return nil, 0, e
			}
		}
		out, e = recheckPinnedPage(ctx, tx, out, where, args)
		if e != nil {
			return nil, 0, e
		}
		if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&total); e != nil {
			return nil, 0, e
		}
	}
	if o.Pinned != nil && actor != "" && r.authority != nil {
		if _, e = r.authority.Authenticate(ctx, r.db, tx, actor); e != nil {
			return nil, 0, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	for i := range out {
		posts.RedactAutomodForEditor(&out[i])
	}
	return out, total, nil
}
