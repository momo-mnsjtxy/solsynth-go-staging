// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"

	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

//go:embed realm_schema.sql
var RealmSchema string

type RealmAssociationProvider interface {
	PostRealmVisibilitySQL(bool) string
	PrelockPublishingScopes(context.Context, *sql.Tx, []string, []string, bool) (map[string]string, error)
	RequirePostRealmMember(context.Context, *sql.Tx, string, string) error
	ResolvePostRealmFilter(context.Context, *sql.Tx, string, string) (string, error)
}

func SameOptionalID(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func RealmIDs(ids ...*string) []string {
	out := []string{}
	for _, id := range ids {
		if id != nil {
			out = append(out, *id)
		}
	}
	return out
}
func RealmPredicate(v VisibilityProvider, member bool) string {
	if realms, ok := v.(RealmAssociationProvider); ok {
		return realms.PostRealmVisibilitySQL(member)
	}
	return `p.realm_id IS NULL`
}
func PrelockPublishingScopes(ctx context.Context, tx *sql.Tx, v VisibilityProvider, publishers, realms []string, deleted bool) (map[string]string, error) {
	if p, ok := v.(RealmAssociationProvider); ok {
		out, e := p.PrelockPublishingScopes(ctx, tx, publishers, realms, deleted)
		if errors.Is(e, accounts.ErrNotFound) {
			e = posts.ErrNotFound
		}
		return out, e
	}
	if len(realms) > 0 {
		return nil, posts.ErrUnsupported
	}
	return PrelockPublisherRealms(ctx, tx, v, publishers)
}
func (r *Repository) prelock(ctx context.Context, tx *sql.Tx, actor string, scope MutationScope, v posts.Patch, create bool) error {
	if locker, ok := r.associations.(AssociationPrelocker); ok {
		return locker.Prelock(ctx, tx, actor, scope, v, create)
	}
	scopes, e := PrelockPublishingScopes(ctx, tx, r.visibility, []string{scope.PublisherID}, RealmIDs(scope.RealmID, v.RealmID), scope.Deleting)
	if e != nil {
		return e
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, scope.PublisherID).Scan(&held); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return accounts.ErrNotFound
		}
		return e
	}
	return accountpg.VerifyPublisherRealms(ctx, tx, scopes)
}
func (r *Repository) authorizeRealms(ctx context.Context, tx *sql.Tx, actor string, publisherType int, old *string, v posts.Patch, deleting bool) error {
	ids := RealmIDs(old, v.RealmID)
	if len(ids) == 0 || deleting {
		return nil
	}
	if publisherType != 0 {
		return posts.ErrUnsupported
	}
	realms, ok := r.visibility.(RealmAssociationProvider)
	if !ok {
		return posts.ErrUnsupported
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if e := realms.RequirePostRealmMember(ctx, tx, actor, id); e != nil {
			return e
		}
	}
	return nil
}
func ResolveRealmFilter(ctx context.Context, tx *sql.Tx, v VisibilityProvider, actor, selector string) (string, error) {
	if selector == "" {
		return "", nil
	}
	realms, ok := v.(RealmAssociationProvider)
	if !ok {
		return "", posts.ErrUnsupported
	}
	return realms.ResolvePostRealmFilter(ctx, tx, actor, selector)
}
