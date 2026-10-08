// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
)

var _ postpg.AssociationPrelocker = (*Repository)(nil)

// Prelock runs after the canonical actor/proof lock, before any source publisher
// lock. Opposite-direction replies acquire the same sorted publisher set; using
// the final source lock mode up front also avoids SHARE-to-UPDATE upgrades.
func (r *Repository) Prelock(ctx context.Context, tx *sql.Tx, actor string, scope postpg.MutationScope, v posts.Patch, create bool) error {
	publisher := scope.PublisherID
	if tx == nil || !authority.ValidID(actor) || !authority.ValidID(publisher) {
		return posts.ErrInvalid
	}
	type targetScope struct {
		publisher string
		realm     *string
	}
	targets := map[string]targetScope{}
	realms := postpg.RealmIDs(scope.RealmID, v.RealmID)
	publishers := map[string]bool{publisher: true}
	targetIDs := []*string{v.RepliedPostID, v.ForwardedPostID, v.ChainedPostID}
	var existingReply *string
	if !create {
		if v.RepliedPostID != nil || v.ForwardedPostID != nil || v.ChainedPostID != nil {
			return posts.ErrUnsupported
		}
		if !scope.Deleting {
			e := tx.QueryRowContext(ctx, `SELECT replied_post_id::text FROM owned_post_relations WHERE post_id=$1`, scope.PostID).Scan(&existingReply)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if existingReply != nil {
				targetIDs = append(targetIDs, existingReply)
			}
		}
	}
	for _, target := range targetIDs {
		if target == nil {
			continue
		}
		if !authority.ValidID(*target) {
			return posts.ErrInvalid
		}
		var id string
		var realm *string
		if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, *target).Scan(&id, &realm); e != nil {
			return missing(e)
		}
		targets[*target] = targetScope{id, realm}
		realms = append(realms, postpg.RealmIDs(realm)...)
		publishers[id] = true
	}
	ordered := make([]string, 0, len(publishers))
	for id := range publishers {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	scopes, e := postpg.PrelockPublishingScopes(ctx, tx, r.visibility, ordered, realms, scope.Deleting)
	if e != nil {
		return e
	}
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
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return e
	}
	ordered = ordered[:0]
	for id := range targets {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		var actual string
		var realm *string
		if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&actual, &realm); e != nil {
			return missing(e)
		}
		if actual != targets[id].publisher || !postpg.SameOptionalID(realm, targets[id].realm) {
			return posts.ErrConflict
		}
	}
	if existingReply != nil {
		ref := targets[*existingReply]
		ids := postpg.RealmIDs(ref.realm)
		if publisherRealm := scopes[ref.publisher]; publisherRealm != "" {
			ids = append(ids, publisherRealm)
		}
		for _, realmID := range ids {
			if e := postpg.RequireRealmAction(ctx, tx, r.visibility, actor, realmID, social.CanComment); e != nil {
				return e
			}
		}
	}
	// Apply retains its editor, visibility, locked/draft and relation checks.
	return nil
}
