// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

// Apply joins a postpg mutation after the canonical actor/publisher/post locks.
// No independent copy of a post or publisher is written by this package.
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v posts.Patch, create bool) error {
	if tx == nil || p == nil {
		return interactions.ErrInvalid
	}
	if p.DeletedAt != nil {
		return nil
	} // Reads filter tombstones; retain historical links.
	if v.CollectionIDs != nil && !create && len(*v.CollectionIDs) > 0 {
		return interactions.ErrUnsupported
	} // Source only applies these at create.
	changed := v.Tags != nil || v.Categories != nil || v.CollectionIDs != nil
	now := r.now().UTC()
	if v.Tags != nil {
		// Same table lock mode as taxonomy create/delete/protection, held through post
		// commit. Prevents duplicate slugs and races with tag claims/event expiration.
		if _, e := tx.ExecContext(ctx, `LOCK TABLE owned_taxonomy.post_tags IN SHARE ROW EXCLUSIVE MODE`); e != nil {
			return e
		}
		slugs := normalized(*v.Tags)
		ids := []string{}
		for _, slug := range slugs {
			var t taxonomy.Record
			t.Slug = slug
			e := tx.QueryRowContext(ctx, `SELECT id::text,owner_publisher_id::text,is_protected,is_event,event_ends_at FROM owned_taxonomy.post_tags WHERE lower(slug)=$1 AND deleted_at IS NULL FOR UPDATE`, slug).Scan(&t.ID, &t.OwnerPublisherID, &t.IsProtected, &t.IsEvent, &t.EventEndsAt)
			if errors.Is(e, sql.ErrNoRows) {
				id, e := newID()
				if e != nil {
					return e
				}
				t.ID = id
				if _, e = tx.ExecContext(ctx, `INSERT INTO owned_taxonomy.post_tags(id,slug,is_protected,is_event,created_at,updated_at)VALUES($1,$2,false,false,$3,$3)`, id, slug, now); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `INSERT INTO owned_taxonomy.actions(actor_id,is_admin,resource_kind,resource_id,operation,occurred_at)VALUES($1,false,'tag',$2,'create',$3)`, actor, id, now); e != nil {
					return e
				}
			} else if e != nil {
				return e
			}
			if t.OwnerPublisherID != nil {
				var individual bool
				if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers WHERE id=$1 AND type=0)`, *t.OwnerPublisherID).Scan(&individual); e != nil {
					return e
				}
				if !individual {
					return interactions.ErrTagUnavailable
				}
			}
			if taxonomy.ValidateTagUsage(t, &p.PublisherID, now) != nil {
				return interactions.ErrTagUnavailable
			}
			ids = append(ids, t.ID)
		}
		if _, e := tx.ExecContext(ctx, `DELETE FROM owned_taxonomy.post_tag_links WHERE posts_id=$1`, p.ID); e != nil {
			return e
		}
		for _, id := range ids {
			if _, e := tx.ExecContext(ctx, `INSERT INTO owned_taxonomy.post_tag_links(posts_id,tags_id)VALUES($1,$2)`, p.ID, id); e != nil {
				return e
			}
		}
	}
	if v.Categories != nil {
		if _, e := tx.ExecContext(ctx, `LOCK TABLE owned_taxonomy.post_categories IN SHARE ROW EXCLUSIVE MODE`); e != nil {
			return e
		}
		ids := []string{}
		for _, slug := range normalized(*v.Categories) {
			var id string
			if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_taxonomy.post_categories WHERE lower(slug)=$1 AND deleted_at IS NULL FOR UPDATE`, slug).Scan(&id); e != nil {
				if errors.Is(e, sql.ErrNoRows) {
					return posts.ErrInvalid
				}
				return e
			}
			ids = append(ids, id)
		}
		if _, e := tx.ExecContext(ctx, `DELETE FROM owned_taxonomy.post_category_links WHERE posts_id=$1`, p.ID); e != nil {
			return e
		}
		for _, id := range ids {
			if _, e := tx.ExecContext(ctx, `INSERT INTO owned_taxonomy.post_category_links(posts_id,categories_id)VALUES($1,$2)`, p.ID, id); e != nil {
				return e
			}
		}
	}
	if v.CollectionIDs != nil {
		ids, e := validIDs(*v.CollectionIDs)
		if e != nil {
			return e
		}
		for _, id := range ids {
			var pub string
			e = tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_post_collections WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&pub)
			if e != nil {
				return missing(e)
			}
			if pub != p.PublisherID {
				return posts.ErrForbidden
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_collection_items(collection_id,post_id,created_at)VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, p.ID, now); e != nil {
				return e
			}
		}
	}
	if changed {
		return audit(ctx, tx, actor, p.ID, "", "post.associate", now)
	}
	return nil
}
func normalized(v []string) []string {
	set := map[string]bool{}
	for _, s := range v {
		s = taxonomy.NormalizeSlug(s)
		if s != "" {
			set[s] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func (r *Repository) RecheckProjection(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	// Load uses fresh canonical metadata predicates but initializes all association
	// fields. Keep its metadata result without erasing the caller's nested posts.
	current := *p
	if e := r.Load(ctx, tx, actor, &current); e != nil {
		return e
	}
	p.Tags, p.Categories = current.Tags, current.Categories
	p.PublisherCollections = current.PublisherCollections
	p.ReactionsCount, p.ReactionsMade = current.ReactionsCount, current.ReactionsMade
	return nil
}

// Load runs entirely in the caller's snapshot, after its visibility check.
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	postpg.EmptyAssociations(p)
	if p.DeletedAt != nil {
		return nil
	}
	if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_post_boosts b JOIN owned_publishers u ON u.id=b.publisher_id WHERE b.post_id=$2 AND `+postpg.VisiblePublisherPredicate(r.visibility, "u", "$1", true), actor, p.ID).Scan(&p.BoostCount); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT t.id::text,t.slug,t.name,t.description,t.owner_publisher_id::text,t.is_protected,t.is_event,t.event_ends_at FROM owned_taxonomy.post_tags t JOIN owned_taxonomy.post_tag_links l ON l.tags_id=t.id WHERE l.posts_id=$1 AND t.deleted_at IS NULL AND (t.owner_publisher_id IS NULL OR EXISTS(SELECT 1 FROM owned_publishers owner WHERE owner.id=t.owner_publisher_id AND owner.type=0 AND owner.deleted_at IS NULL AND owner.shadowbanned_at IS NULL AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=owner.account_id AND NOT a.disabled))) ORDER BY t.slug,t.id`, p.ID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var t taxonomy.TagProjection
		if e = rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Description, &t.OwnerPublisherID, &t.IsProtected, &t.IsEvent, &t.EventEndsAt); e != nil {
			rows.Close()
			return e
		}
		p.Tags = append(p.Tags, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	rows, e = tx.QueryContext(ctx, `SELECT c.id::text,c.slug,c.name FROM owned_taxonomy.post_categories c JOIN owned_taxonomy.post_category_links l ON l.categories_id=c.id WHERE l.posts_id=$1 AND c.deleted_at IS NULL ORDER BY c.slug,c.id`, p.ID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var c taxonomy.CategoryProjection
		if e = rows.Scan(&c.ID, &c.Slug, &c.Name); e != nil {
			rows.Close()
			return e
		}
		p.Categories = append(p.Categories, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	rows, e = tx.QueryContext(ctx, `SELECT `+collectionColumns+collectionFrom+` AND c.publisher_id=$2 AND EXISTS(SELECT 1 FROM owned_post_collection_items i WHERE i.collection_id=c.id AND i.post_id=$1) ORDER BY c.slug,c.id`, p.ID, p.PublisherID)
	if e != nil {
		return e
	}
	for rows.Next() {
		c, e := scanCollection(rows)
		if e != nil {
			rows.Close()
			return e
		}
		p.PublisherCollections = append(p.PublisherCollections, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	rows, e = tx.QueryContext(ctx, `SELECT r.symbol,count(*),bool_or(r.account_id=NULLIF($2,'')::uuid) FROM owned_post_reactions r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.post_id=$1 AND NOT a.disabled GROUP BY r.symbol`, p.ID, actor)
	if e != nil {
		return e
	}
	for rows.Next() {
		var s string
		var count int64
		var made sql.NullBool
		if e = rows.Scan(&s, &count, &made); e != nil {
			rows.Close()
			return e
		}
		p.ReactionsCount[s] = count
		if made.Valid && made.Bool {
			p.ReactionsMade[s] = true
		}
	}
	e = rows.Err()
	rows.Close()
	return e
}
