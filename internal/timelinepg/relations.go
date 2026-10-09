// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"sort"
)

const maxDepth = 50

// SupportsPostRelations marks this adapter for canonical lifecycle input gating.
func (r *Repository) SupportsPostRelations() {}

// Apply is called only inside the canonical post transaction, after proof,
// actor, ordered publisher prelocks, publisher-editor and post locks. Relations
// are immutable after creation.
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v posts.Patch, create bool) error {
	if tx == nil || p == nil || !authority.ValidID(actor) {
		return posts.ErrInvalid
	}
	if p.DeletedAt != nil {
		if r.associations != nil {
			return r.associations.Apply(ctx, tx, actor, p, v, create)
		}
		return nil
	}
	changed := v.RepliedPostID != nil || v.ForwardedPostID != nil || v.ChainedPostID != nil
	if changed && !create {
		return posts.ErrUnsupported
	}
	if changed {
		targets := []string{}
		for _, s := range []*string{v.RepliedPostID, v.ForwardedPostID, v.ChainedPostID} {
			if s != nil {
				if !authority.ValidID(*s) || *s == p.ID {
					return posts.ErrInvalid
				}
				targets = append(targets, *s)
			}
		}
		sort.Strings(targets)
		loaded := map[string]posts.Post{}
		for _, target := range targets {
			var pub string
			if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, target).Scan(&pub); e != nil {
				return missing(e)
			}
			// The source actor lock serializes blocks involving the writer. Referenced
			// publisher and post locks serialize deletion and visibility changes.
			var held string
			if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, pub).Scan(&held); e != nil {
				return missing(e)
			}
			if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, target).Scan(&held); e != nil {
				return missing(e)
			}
			targetPost, e := postpg.ReadWithVisibility(ctx, tx, actor, target, r.visibility)
			if e != nil {
				return e
			}
			if targetPost.DraftedAt != nil {
				return posts.ErrNotFound
			}
			if targetPost.LockedAt != nil {
				return posts.ErrLocked
			}
			loaded[target] = targetPost
		}
		if v.ChainedPostID != nil {
			target := loaded[*v.ChainedPostID]
			if target.PublisherID != p.PublisherID {
				return posts.ErrForbidden
			}
			var head *string
			e := tx.QueryRowContext(ctx, `SELECT chained_post_id::text FROM owned_post_relations WHERE post_id=$1`, target.ID).Scan(&head)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if head != nil {
				hp, e := postpg.ReadWithVisibility(ctx, tx, actor, *head, r.visibility)
				if e != nil {
					return e
				}
				if hp.PublisherID != p.PublisherID {
					return posts.ErrForbidden
				}
				v.ChainedPostID = head
			}
		}
		if v.RepliedPostID != nil {
			target := loaded[*v.RepliedPostID]
			for _, rid := range []*string{target.RealmID, target.Publisher.RealmID} {
				if rid != nil {
					if e := postpg.RequireRealmAction(ctx, tx, r.visibility, actor, *rid, social.CanComment); e != nil {
						return e
					}
				}
			}
			var depth int
			var cycle bool
			e := tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,path,cycle) AS (SELECT $1::uuid,ARRAY[$1::uuid],false UNION ALL SELECT l.replied_post_id,a.path||l.replied_post_id,l.replied_post_id=ANY(a.path) FROM ancestors a JOIN owned_post_relations l ON l.post_id=a.id WHERE l.replied_post_id IS NOT NULL AND cardinality(a.path)<51 AND NOT a.cycle) SELECT COALESCE(max(cardinality(path)),0),COALESCE(bool_or(cycle),false) FROM ancestors`, *v.RepliedPostID).Scan(&depth, &cycle)
			if e != nil {
				return e
			}
			if cycle || depth >= maxDepth {
				return timeline.ErrDepth
			}
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_post_relations(post_id,replied_post_id,forwarded_post_id,chained_post_id)VALUES($1,$2,$3,$4)`, p.ID, v.RepliedPostID, v.ForwardedPostID, v.ChainedPostID); e != nil {
			return e
		}
		if e := audit(ctx, tx, actor, "post.relate", p.ID, r.now().UTC()); e != nil {
			return e
		}
	}
	if r.associations != nil {
		return r.associations.Apply(ctx, tx, actor, p, v, create)
	}
	for _, s := range []*[]string{v.Tags, v.Categories, v.CollectionIDs} {
		if s != nil && len(*s) > 0 {
			return posts.ErrUnsupported
		}
	}
	return nil
}
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	if tx == nil || p == nil {
		return posts.ErrInvalid
	}
	if r.visibility == nil {
		return posts.ErrUnavailable
	}
	if p.DeletedAt == nil {
		if _, e := postpg.ReadWithVisibility(ctx, tx, actor, p.ID, r.visibility); e != nil {
			return e
		}
	}
	if r.associations != nil {
		if e := r.associations.Load(ctx, tx, actor, p); e != nil {
			return e
		}
	} else {
		postpg.EmptyAssociations(p)
	}
	p.RepliedPostID = nil
	p.ForwardedPostID = nil
	p.ChainedPostID = nil
	p.RepliedPost = nil
	p.ForwardedPost = nil
	p.ChainedPost = nil
	p.RepliesCount = 0
	p.ForwardsCount = 0
	p.ThreadRepliesCount = 0
	p.ChainedCount = 0
	p.ChainedPosts = []posts.Post{}
	if p.DeletedAt != nil {
		return nil
	}
	var reply, forward, chain *string
	e := tx.QueryRowContext(ctx, `SELECT replied_post_id::text,forwarded_post_id::text,chained_post_id::text FROM owned_post_relations WHERE post_id=$1`, p.ID).Scan(&reply, &forward, &chain)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	for _, link := range []struct {
		id   *string
		out  **string
		post **posts.Post
	}{{reply, &p.RepliedPostID, &p.RepliedPost}, {forward, &p.ForwardedPostID, &p.ForwardedPost}, {chain, &p.ChainedPostID, &p.ChainedPost}} {
		if link.id == nil {
			continue
		}
		parent, e := postpg.ReadWithVisibility(ctx, tx, actor, *link.id, r.visibility)
		if errors.Is(e, posts.ErrNotFound) {
			continue
		}
		if e != nil {
			return e
		}
		// One level only; each target is independently checked. Hidden identifiers
		// are redacted along with the nested projection, including deleted targets.
		if r.associations != nil {
			if e = r.associations.Load(ctx, tx, actor, &parent); e != nil {
				return e
			}
		}
		*link.out = link.id
		*link.post = &parent
	}
	for _, count := range []struct {
		column string
		dst    *int64
	}{{"replied_post_id", &p.RepliesCount}, {"forwarded_post_id", &p.ForwardsCount}, {"chained_post_id", &p.ChainedCount}} {
		e = tx.QueryRowContext(ctx, `SELECT count(*)`+r.base()+` AND EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.`+count.column+`=$2)`, actor, p.ID).Scan(count.dst)
		if e != nil {
			return e
		}
	}
	if p.ChainedCount > 0 {
		rows, e := tx.QueryContext(ctx, `SELECT `+postpg.ProjectionColumns+r.base()+` AND EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.chained_post_id=$2) ORDER BY p.published_at,p.id LIMIT 100`, actor, p.ID)
		if e != nil {
			return e
		}
		children := []posts.Post{}
		for rows.Next() {
			child, e := postpg.ScanProjection(rows)
			if e != nil {
				rows.Close()
				return e
			}
			children = append(children, child)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for i := range children {
			if r.associations != nil {
				if e = r.associations.Load(ctx, tx, actor, &children[i]); e != nil {
					return e
				}
			}
			head := p.ID
			children[i].ChainedPostID = &head
		}
		p.ChainedPosts = children
	}
	return tx.QueryRowContext(ctx, r.descendantCTE()+` SELECT count(*) FROM tree`, actor, p.ID).Scan(&p.ThreadRepliesCount)
}
func (r *Repository) descendantCTE() string {
	return `WITH RECURSIVE visible AS MATERIALIZED(SELECT p.id,p.published_at` + postpg.ActivePostFrom + ` AND ` + postpg.ListingPredicate(r.visibility) + ` AND EXISTS(SELECT 1 FROM owned_post_relations z WHERE z.post_id=p.id AND z.replied_post_id IS NOT NULL)), links AS (SELECT v.id,l.replied_post_id,v.published_at FROM visible v JOIN owned_post_relations l ON l.post_id=v.id), tree(id,parent,depth,path,ordering) AS (SELECT id,replied_post_id,0,ARRAY[id],ARRAY[row_number() OVER(ORDER BY published_at DESC,id DESC)] FROM links WHERE replied_post_id=$2::uuid UNION ALL SELECT l.id,l.replied_post_id,t.depth+1,t.path||l.id,t.ordering||row_number() OVER(PARTITION BY l.replied_post_id ORDER BY l.published_at DESC,l.id DESC) FROM tree t JOIN links l ON l.replied_post_id=t.id WHERE t.depth<49 AND NOT l.id=ANY(t.path))`
}

func (r *Repository) SupportsPostAttachments() bool {
	v, ok := r.associations.(interface{ SupportsPostAttachments() bool })
	return ok && v.SupportsPostAttachments()
}

func (r *Repository) SupportsPostComposition() bool {
	v, ok := r.associations.(interface{ SupportsPostComposition() bool })
	return ok && v.SupportsPostComposition()
}
func (r *Repository) CheckPostPublication(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, lock bool) error {
	if a, ok := r.associations.(postpg.PublicationAssociation); ok {
		return a.CheckPostPublication(ctx, tx, actor, p, lock)
	}
	return nil
}
