// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"errors"

	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
)

const collectionPostOrder = `COALESCE(p.published_at,p.created_at) DESC,p.created_at DESC,p.id DESC`

func adjacentMissing(next bool) error {
	if next {
		return interactions.ErrNextCollectionPost
	}
	return interactions.ErrPrevCollectionPost
}

// Both the anchor and its neighbor must occur in the same visible ordered set.
// Filtering after computing a neighbor would leak hidden anchors or skip a
// visible neighbor when an intervening row is private, drafted or moderated.
func (r *Repository) adjacentID(ctx context.Context, tx *sql.Tx, actor, collection, anchor string, next bool) (string, error) {
	direction := "lag"
	if next {
		direction = "lead"
	}
	var id *string
	e := tx.QueryRowContext(ctx, `WITH visible AS (SELECT p.id,`+direction+`(p.id) OVER(ORDER BY `+collectionPostOrder+`) AS adjacent`+postpg.ActivePostFrom+` AND `+postpg.ListingPredicate(r.visibility)+` AND EXISTS(SELECT 1 FROM owned_post_collection_items i WHERE i.collection_id=$2 AND i.post_id=p.id)) SELECT adjacent::text FROM visible WHERE id=$3`, actor, collection, anchor).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) || e == nil && id == nil {
		return "", adjacentMissing(next)
	}
	if e != nil {
		return "", e
	}
	return *id, nil
}

func (r *Repository) AdjacentCollectionPost(ctx context.Context, actor, publisher, slug, anchor string, next bool) (out posts.Post, e error) {
	if !authority.ValidID(anchor) {
		return out, adjacentMissing(next)
	}
	var dependencies *collectionDependencyPlan
	var id string
	e = r.collectionTransaction(ctx, actor, publisher, slug, "", false, func(tx *sql.Tx, c posts.Collection) error {
		if x := dependencies.verify(ctx, tx, c.ID, id); x != nil {
			return x
		}
		if x := dependencies.lockPostsAndMedia(ctx, tx); x != nil {
			return x
		}
		if x := dependencies.verify(ctx, tx, c.ID, id); x != nil {
			return x
		}
		var x error
		if x = r.collectionAuthority(ctx, tx, actor, "", false); x != nil {
			return x
		}
		current, x := r.adjacentID(ctx, tx, actor, c.ID, anchor, next)
		if x != nil {
			return x
		}
		if current != id {
			return posts.ErrConflict
		}
		out, x = postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility)
		if x != nil {
			return x
		}
		if r.projection != nil {
			x = r.projection.Load(ctx, tx, actor, &out)
		} else {
			x = r.Load(ctx, tx, actor, &out)
		}
		if x != nil {
			return x
		}
		// A custom projection may wait. The host's canonical chain never does, but
		// stale visible anchors and current proof are still checked after hydration.
		current, x = r.adjacentID(ctx, tx, actor, c.ID, anchor, next)
		if x != nil {
			return x
		}
		if current != id {
			return posts.ErrConflict
		}
		return dependencies.verify(ctx, tx, c.ID, id)
	}, func(tx *sql.Tx, collection string) (*collectionDependencyPlan, error) {
		var x error
		id, x = r.adjacentID(ctx, tx, actor, collection, anchor, next)
		if x != nil {
			return nil, x
		}
		dependencies, x = observeCollectionDependencies(ctx, tx, collection, id)
		return dependencies, x
	})
	if e != nil {
		return posts.Post{}, e
	}
	return
}
