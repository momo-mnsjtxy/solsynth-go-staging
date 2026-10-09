// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func (r *Repository) PostRealmVisibilitySQL(member bool) string {
	joined := `EXISTS(SELECT 1 FROM owned_realm_members rm JOIN owned_authority_accounts viewer ON viewer.id=rm.account_id WHERE rm.realm_id=realm.id AND rm.account_id=NULLIF($1::text,'')::uuid AND rm.joined_at IS NOT NULL AND rm.leave_at IS NULL AND NOT viewer.disabled)`
	if !member {
		joined = `(realm.is_public OR ` + joined + `)`
	}
	return `(p.realm_id IS NULL OR EXISTS(SELECT 1 FROM owned_realms realm JOIN owned_authority_accounts realm_owner ON realm_owner.id=realm.account_id WHERE realm.id=p.realm_id AND realm.deleted_at IS NULL AND NOT realm_owner.disabled AND ` + joined + `))`
}
func (r *Repository) RequirePostRealmMember(ctx context.Context, tx *sql.Tx, actor, id string) error {
	if tx == nil || !authority.ValidID(actor) || !authority.ValidID(id) {
		return posts.ErrForbidden
	}
	v, e := r.realm(ctx, tx, id, false)
	if errors.Is(e, social.ErrNotFound) {
		return posts.ErrNotFound
	}
	if e != nil {
		return e
	}
	_, e = requireRole(ctx, tx, v.ID, actor, social.Normal)
	if errors.Is(e, social.ErrForbidden) || errors.Is(e, social.ErrNotFound) {
		return posts.ErrForbidden
	}
	return e
}
func (r *Repository) ResolvePostRealmFilter(ctx context.Context, tx *sql.Tx, actor, selector string) (string, error) {
	if tx == nil {
		return "", posts.ErrUnavailable
	}
	if len(selector) == 0 || len(selector) > 256 {
		return "", posts.ErrInvalid
	}
	match := `lower(realm.slug)=lower($2)`
	if authority.ValidID(selector) {
		match = `realm.id=$2::uuid`
	}
	var id string
	e := tx.QueryRowContext(ctx, `SELECT realm.id::text FROM owned_realms realm JOIN owned_authority_accounts owner ON owner.id=realm.account_id WHERE `+match+` AND realm.deleted_at IS NULL AND NOT owner.disabled AND (realm.is_public OR EXISTS(SELECT 1 FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.realm_id=realm.id AND m.account_id=NULLIF($1::text,'')::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND NOT a.disabled))`, actor, selector).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		e = posts.ErrNotFound
	}
	return id, e
}

// RequirePostRealmModerator follows the authored pin controller's role check.
// The caller holds the realm row lock through commit, serializing membership
// withdrawal and demotion with the post transition.
func (r *Repository) RequirePostRealmModerator(ctx context.Context, tx *sql.Tx, actor, id string) error {
	if tx == nil || !authority.ValidID(actor) || !authority.ValidID(id) {
		return posts.ErrForbidden
	}
	if e := r.RequirePostRealmMember(ctx, tx, actor, id); e != nil {
		return e
	}
	_, e := requireRole(ctx, tx, id, actor, social.Moderator)
	if errors.Is(e, social.ErrForbidden) || errors.Is(e, social.ErrNotFound) {
		return posts.ErrForbidden
	}
	return e
}
