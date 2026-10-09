// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// BatchPrelock acquires the union before any per-post lifecycle work. All source
// and related publishers/posts use the final exclusive mode in sorted order,
// preventing reversed batches and reply-target upgrades from forming cycles.
func (r *Repository) BatchPrelock(ctx context.Context, tx *sql.Tx, actor string, scopes []postpg.MutationScope) error {
	if tx == nil || len(scopes) == 0 {
		return posts.ErrInvalid
	}
	pubs := map[string]bool{}
	targets := map[string]postpg.MutationScope{}
	realms := []string{}
	deleting := scopes[0].Deleting
	for _, scope := range scopes {
		pubs[scope.PublisherID] = true
		targets[scope.PostID] = scope
		realms = append(realms, postpg.RealmIDs(scope.RealmID)...)
		if deleting {
			continue
		}
		var reply, forward, chain *string
		e := tx.QueryRowContext(ctx, `SELECT replied_post_id::text,forwarded_post_id::text,chained_post_id::text FROM owned_post_relations WHERE post_id=$1`, scope.PostID).Scan(&reply, &forward, &chain)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		for _, id := range []*string{reply, forward, chain} {
			if id == nil {
				continue
			}
			if _, ok := targets[*id]; ok {
				continue
			}
			var target postpg.MutationScope
			target.PostID = *id
			if e = tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, *id).Scan(&target.PublisherID, &target.RealmID); e != nil {
				return missing(e)
			}
			targets[*id] = target
			pubs[target.PublisherID] = true
			realms = append(realms, postpg.RealmIDs(target.RealmID)...)
		}
	}
	ordered := make([]string, 0, len(pubs))
	for id := range pubs {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	ancestry, e := postpg.PrelockPublishingScopes(ctx, tx, r.visibility, ordered, realms, deleting)
	if e != nil {
		return e
	}
	for _, id := range ordered {
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&held); e != nil {
			return missing(e)
		}
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, ancestry); e != nil {
		return e
	}
	ordered = ordered[:0]
	for id := range targets {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		var pub string
		var realm *string
		if e = tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&pub, &realm); e != nil {
			return missing(e)
		}
		if pub != targets[id].PublisherID || !postpg.SameOptionalID(realm, targets[id].RealmID) {
			return posts.ErrConflict
		}
	}
	return nil
}
