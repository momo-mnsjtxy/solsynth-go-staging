// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"sort"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
)

// The alias and actor SQL placeholders are compile-time adapter arguments. No
// user input enters these fragments. Pending invitations confer no membership.
func (r *Repository) realmPublisherPredicate(alias, actor string, public bool) string {
	member := `EXISTS(SELECT 1 FROM owned_realm_members rm JOIN owned_authority_accounts viewer ON viewer.id=rm.account_id WHERE rm.realm_id=realm.id AND rm.account_id=NULLIF(` + actor + `::text,'')::uuid AND rm.joined_at IS NOT NULL AND rm.leave_at IS NULL AND NOT viewer.disabled)`
	if public {
		member = `(realm.is_public OR ` + member + `)`
	}
	return `(` + alias + `.type=0 OR (` + alias + `.type=1 AND EXISTS(SELECT 1 FROM owned_realms realm JOIN owned_authority_accounts realm_owner ON realm_owner.id=realm.account_id WHERE realm.id=` + alias + `.realm_id AND realm.deleted_at IS NULL AND NOT realm_owner.disabled AND ` + member + `)))`
}
func (r *Repository) PublisherVisibilitySQL(alias, actor string) string {
	return r.realmPublisherPredicate(alias, actor, true)
}
func (r *Repository) PublisherMembershipSQL(alias, actor string) string {
	return r.realmPublisherPredicate(alias, actor, false)
}

func (r *Repository) PrelockPublisherRealms(ctx context.Context, tx *sql.Tx, ids []string) (map[string]string, error) {
	if tx == nil {
		return nil, accounts.ErrUnavailable
	}
	expected := make(map[string]string, len(ids))
	realms := map[string]bool{}
	for _, id := range ids {
		if !authority.ValidID(id) {
			return nil, accounts.ErrNotFound
		}
		var realm string
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(realm_id::text,'') FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&realm); e != nil {
			if e == sql.ErrNoRows {
				return nil, accounts.ErrNotFound
			}
			return nil, e
		}
		expected[id] = realm
		if realm != "" {
			realms[realm] = true
		}
	}
	ordered := make([]string, 0, len(realms))
	for id := range realms {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		var held string
		e := tx.QueryRowContext(ctx, `SELECT r.id::text FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.id=$1 AND r.deleted_at IS NULL AND NOT a.disabled FOR SHARE OF r`, id).Scan(&held)
		if e == sql.ErrNoRows {
			return nil, accounts.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
	}
	return expected, nil
}

func (r *Repository) ResolveRealmForPublisher(ctx context.Context, tx *sql.Tx, actor, selector string) (string, error) {
	if tx == nil || !authority.ValidID(actor) {
		return "", accounts.ErrForbidden
	}
	match := `lower(r.slug)=lower($1)`
	if authority.ValidID(selector) {
		match = `r.id=$1::uuid`
	}
	v, e := scanRealm(tx.QueryRowContext(ctx, `SELECT `+realmColumns+` FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.deleted_at IS NULL AND NOT a.disabled AND `+match+` FOR UPDATE OF r`, selector))
	if e != nil {
		return "", publisherRealmError(e)
	}
	if _, e = requireRole(ctx, tx, v.ID, actor, social.Moderator); e != nil {
		return "", publisherRealmError(e)
	}
	return v.ID, nil
}
func publisherRealmError(e error) error {
	if e == social.ErrNotFound {
		return accounts.ErrNotFound
	}
	if e == social.ErrForbidden {
		return accounts.ErrForbidden
	}
	return e
}
