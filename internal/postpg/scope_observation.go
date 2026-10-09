// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
	"sort"
)

// ObservePostScopes resolves references before realm or publisher locks. Callers
// must restrict source publishers, acquire their lock set, and verify afterward.
func ObservePostScopes(ctx context.Context, tx *sql.Tx, ids []string) (map[string]MutationScope, error) {
	out := map[string]MutationScope{}
	for _, id := range ids {
		if !authority.ValidID(id) {
			return nil, posts.ErrNotFound
		}
		if _, ok := out[id]; ok {
			continue
		}
		s := MutationScope{PostID: id}
		if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&s.PublisherID, &s.RealmID); e != nil {
			return nil, mapped(e)
		}
		out[id] = s
	}
	return out, nil
}
func ObservedRealmIDs(scopes map[string]MutationScope) []string {
	out := []string{}
	for _, s := range scopes {
		out = append(out, RealmIDs(s.RealmID)...)
	}
	return out
}
func VerifyPostScopes(ctx context.Context, tx *sql.Tx, scopes map[string]MutationScope) error {
	ids := make([]string, 0, len(scopes))
	for id := range scopes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var publisher string
		var realm *string
		if e := tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&publisher, &realm); e != nil {
			return mapped(e)
		}
		if publisher != scopes[id].PublisherID || !SameOptionalID(realm, scopes[id].RealmID) {
			return posts.ErrConflict
		}
	}
	return nil
}
