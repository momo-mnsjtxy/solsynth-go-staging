// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

const collectionColumns = `c.id::text,c.publisher_id::text,c.slug,c.name,c.description,c.created_at,c.updated_at,c.deleted_at,u.id::text,u.name,u.nick,u.bio,u.type,u.account_id::text,u.realm_id::text,u.created_at,u.updated_at,u.verification`
const collectionFrom = ` FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id WHERE c.deleted_at IS NULL AND u.deleted_at IS NULL AND u.type=0 AND (u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=u.account_id AND NOT a.disabled))`

func scanCollection(s interface{ Scan(...any) error }) (posts.Collection, error) {
	var c posts.Collection
	var verification []byte
	e := s.Scan(&c.ID, &c.PublisherID, &c.Slug, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt, &c.Publisher.ID, &c.Publisher.Name, &c.Publisher.Nick, &c.Publisher.Bio, &c.Publisher.Type, &c.Publisher.AccountID, &c.Publisher.RealmID, &c.Publisher.CreatedAt, &c.Publisher.UpdatedAt, &verification)
	if errors.Is(e, sql.ErrNoRows) {
		e = interactions.ErrCollectionNotFound
	}
	if e == nil {
		c.Publisher.Verification, e = accounts.DecodeVerification(verification)
	}
	return c, e
}
func getCollection(ctx context.Context, tx *sql.Tx, publisher, slug string, lock bool) (posts.Collection, error) {
	q := `SELECT ` + collectionColumns + collectionFrom + ` AND ` + accountpg.PublisherNamePredicate("u", "$1") + ` AND c.slug=$2`
	if lock {
		q += ` FOR UPDATE OF c`
	}
	return scanCollection(tx.QueryRowContext(ctx, q, publisher, taxonomy.NormalizeSlug(slug)))
}
func (r *Repository) Collection(ctx context.Context, actor, publisher, slug string) (posts.Collection, error) {
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return posts.Collection{}, e
	}
	defer tx.Rollback()
	v, e := getCollection(ctx, tx, publisher, slug, false)
	if e != nil {
		return v, e
	}
	allowed, e := postpg.CanViewPublisher(ctx, tx, actor, v.PublisherID, r.visibility)
	if e != nil {
		return posts.Collection{}, e
	}
	if !allowed {
		return posts.Collection{}, interactions.ErrCollectionNotFound
	}
	if e = tx.Commit(); e != nil {
		return posts.Collection{}, e
	}
	return v, nil
}
func (r *Repository) Collections(ctx context.Context, actor, publisher string, p interactions.Page) ([]posts.Collection, int64, error) {
	if !interactions.ValidPage(p) {
		return nil, 0, interactions.ErrInvalid
	}
	q := strings.TrimSpace(p.Query)
	// >=3-character source search requires pg_trgm similarity. Fail explicitly
	// until its installation and exact threshold policy are configured.
	if taxonomy.UTF16Length(q) >= 3 {
		return nil, 0, interactions.ErrUnsupported
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	where := collectionFrom + ` AND ($1='' OR ` + accountpg.PublisherNamePredicate("u", "$1") + `) AND ($2='' OR c.slug ILIKE $3 OR c.name ILIKE $3 OR c.description ILIKE $3 OR u.name ILIKE $3 OR u.nick ILIKE $3)`
	where += ` AND ` + accountpg.PublisherModerationPredicate("u", "$4", `u.type=0`)
	args := []any{publisher, q, "%" + q + "%", actor}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	paging := ""
	if publisher == "" {
		paging = ` OFFSET $5 LIMIT $6`
		args = append(args, p.Offset, p.Take)
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+collectionColumns+where+` ORDER BY coalesce(c.name,c.slug),c.slug,c.id`+paging, args...)
	if e != nil {
		return nil, 0, e
	}
	out := []posts.Collection{}
	for rows.Next() {
		v, e := scanCollection(rows)
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
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}
func (r *Repository) CreateCollection(ctx context.Context, actor, publisher string, v interactions.CollectionRequest) (posts.Collection, error) {
	var out posts.Collection
	if e := interactions.ValidateCollection(v, true); e != nil {
		return out, e
	}
	e := r.write(ctx, actor, "", func(tx *sql.Tx, now time.Time) error {
		pub, e := r.publishers.RequirePostEditor(ctx, tx, actor, publisher)
		if e != nil {
			return e
		}
		if pub.Type != 0 {
			return interactions.ErrUnsupported
		}
		id, e := newID()
		if e != nil {
			return e
		}
		out = posts.Collection{ID: id, PublisherID: pub.ID, Publisher: pub, Slug: taxonomy.NormalizeSlug(v.Slug), Name: v.Name, Description: v.Description, CreatedAt: now, UpdatedAt: now}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_collections(id,publisher_id,slug,name,description,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$6)`, id, pub.ID, out.Slug, v.Name, v.Description, now)
		if conflict(e) {
			return interactions.ErrCollectionConflict
		}
		if e != nil {
			return e
		}
		return audit(ctx, tx, actor, "", id, "collection.create", now)
	})
	if e != nil {
		return posts.Collection{}, e
	}
	return out, nil
}
func (r *Repository) lockedCollection(ctx context.Context, tx *sql.Tx, actor, publisher, slug string) (posts.Collection, error) {
	pub, e := r.publishers.RequirePostEditor(ctx, tx, actor, publisher)
	if e != nil {
		return posts.Collection{}, e
	}
	if pub.Type != 0 {
		return posts.Collection{}, interactions.ErrUnsupported
	}
	return getCollection(ctx, tx, pub.Name, slug, true)
}
func (r *Repository) UpdateCollection(ctx context.Context, actor, publisher, slug string, v interactions.CollectionRequest) (posts.Collection, error) {
	var out posts.Collection
	if e := interactions.ValidateCollection(v, false); e != nil {
		return out, e
	}
	e := r.write(ctx, actor, "", func(tx *sql.Tx, now time.Time) error {
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		// Source collection update replaces both nullable fields, unlike post patch.
		c.Name = v.Name
		c.Description = v.Description
		c.UpdatedAt = now
		if _, e = tx.ExecContext(ctx, `UPDATE owned_post_collections SET name=$2,description=$3,updated_at=$4 WHERE id=$1`, c.ID, c.Name, c.Description, now); e != nil {
			return e
		}
		out = c
		return audit(ctx, tx, actor, "", c.ID, "collection.update", now)
	})
	if e != nil {
		return posts.Collection{}, e
	}
	return out, nil
}
func (r *Repository) DeleteCollection(ctx context.Context, actor, publisher, slug string) error {
	return r.write(ctx, actor, "", func(tx *sql.Tx, now time.Time) error {
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_post_collections SET deleted_at=$2,updated_at=$2 WHERE id=$1`, c.ID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_post_collection_items WHERE collection_id=$1`, c.ID); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "", c.ID, "collection.delete", now)
	})
}
func validIDs(ids []string) ([]string, error) {
	if len(ids) > 100 {
		return nil, interactions.ErrInvalid
	}
	set := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if !authority.ValidID(id) {
			return nil, interactions.ErrInvalid
		}
		if !set[id] {
			set[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (r *Repository) AddCollectionPosts(ctx context.Context, actor, publisher, slug string, ids []string, batch bool) error {
	ids, e := validIDs(ids)
	if e != nil {
		return e
	}
	if !batch && len(ids) != 1 {
		return interactions.ErrInvalid
	}
	return r.write(ctx, actor, "post.collections.posts.manage", func(tx *sql.Tx, now time.Time) error {
		pubID, e := r.publishers.ObservePostPublisherID(ctx, tx, actor, publisher)
		if e != nil {
			return e
		}
		observed, e := postpg.ObservePostScopes(ctx, tx, ids)
		if e != nil {
			return e
		}
		for _, s := range observed {
			if s.PublisherID != pubID {
				return interactions.ErrUnsupported
			}
		}
		scopes, e := postpg.PrelockPublishingScopes(ctx, tx, r.visibility, []string{pubID}, postpg.ObservedRealmIDs(observed), false)
		if e != nil {
			return e
		}
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, pubID).Scan(&held); e != nil {
			return missing(e)
		}
		if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
			return e
		}
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		if c.PublisherID != pubID {
			return posts.ErrConflict
		}
		if e = postpg.VerifyPostScopes(ctx, tx, observed); e != nil {
			return e
		}
		for _, id := range ids {
			// Bounded collection flow only includes posts from its canonical publisher.
			// Cross-publisher collection inserts require a separately reviewed policy.
			var actual string
			e = tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&actual)
			if e != nil {
				return missing(e)
			}
			if actual != c.PublisherID {
				return interactions.ErrUnsupported
			}
			p, e := postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility)
			if e != nil {
				return e
			}
			if p.LockedAt != nil {
				return posts.ErrLocked
			}
			res, e := tx.ExecContext(ctx, `INSERT INTO owned_post_collection_items(collection_id,post_id,created_at)VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.ID, id, now)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				if batch {
					continue
				}
				return interactions.ErrDuplicate
			}
			if e = audit(ctx, tx, actor, id, c.ID, "collection.add", now); e != nil {
				return e
			}
		}
		return nil
	})
}
func (r *Repository) RemoveCollectionPosts(ctx context.Context, actor, publisher, slug string, ids []string) error {
	ids, e := validIDs(ids)
	if e != nil {
		return e
	}
	return r.write(ctx, actor, "post.collections.posts.manage", func(tx *sql.Tx, now time.Time) error {
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		for _, id := range ids {
			res, e := tx.ExecContext(ctx, `DELETE FROM owned_post_collection_items WHERE collection_id=$1 AND post_id=$2`, c.ID, id)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n > 0 {
				if e = audit(ctx, tx, actor, id, c.ID, "collection.remove", now); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
func (r *Repository) CollectionPosts(ctx context.Context, actor, publisher, slug string, p interactions.Page) ([]posts.Post, int64, error) {
	if !interactions.ValidPage(p) {
		return nil, 0, interactions.ErrInvalid
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	c, e := getCollection(ctx, tx, publisher, slug, false)
	if e != nil {
		return nil, 0, e
	}
	out, total, e := r.listPosts(ctx, tx, actor, ` AND EXISTS(SELECT 1 FROM owned_post_collection_items i WHERE i.post_id=p.id AND i.collection_id=$2)`, `p.published_at DESC,p.created_at DESC,p.id DESC`, []any{actor, c.ID}, p)
	if e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}
