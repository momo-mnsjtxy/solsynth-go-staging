// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"sort"

	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
)

var _ postpg.AssociationPrelocker = (*Repository)(nil)

// Prelock runs after the canonical actor/proof lock, before any source publisher
// lock. Opposite-direction replies acquire the same sorted publisher set; using
// the final source lock mode up front also avoids SHARE-to-UPDATE upgrades.
func (r *Repository) Prelock(ctx context.Context, tx *sql.Tx, actor, publisher string, v posts.Patch, create bool) error {
	if tx == nil || !authority.ValidID(actor) || !authority.ValidID(publisher) {
		return posts.ErrInvalid
	}
	targets := map[string]string{}
	publishers := map[string]bool{publisher: true}
	for _, target := range []*string{v.RepliedPostID, v.ForwardedPostID, v.ChainedPostID} {
		if target == nil {
			continue
		}
		if !create {
			return posts.ErrUnsupported
		}
		if !authority.ValidID(*target) {
			return posts.ErrInvalid
		}
		var id string
		if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, *target).Scan(&id); e != nil {
			return missing(e)
		}
		targets[*target] = id
		publishers[id] = true
	}
	ordered := make([]string, 0, len(publishers))
	for id := range publishers {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		mode := ` FOR SHARE`
		if id == publisher {
			mode = ` FOR UPDATE`
		}
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL`+mode, id).Scan(&held); e != nil {
			return missing(e)
		}
	}
	ordered = ordered[:0]
	for id := range targets {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		var actual string
		if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&actual); e != nil {
			return missing(e)
		}
		if actual != targets[id] {
			return posts.ErrConflict
		}
	}
	// Apply retains its editor, visibility, locked/draft and relation checks.
	return nil
}
