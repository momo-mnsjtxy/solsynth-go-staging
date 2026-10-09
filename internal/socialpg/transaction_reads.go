// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
)

// RealmForViewerInTransaction takes a canonical ID, never a name selector. The
// caller authenticates; the original active-owner and realm visibility gates run.
func (r *Repository) RealmForViewerInTransaction(ctx context.Context, tx *sql.Tx, viewer, id string) (social.Realm, error) {
	if tx == nil || !authority.ValidID(viewer) || !authority.ValidID(id) {
		return social.Realm{}, social.ErrInvalid
	}
	v, e := scanRealm(tx.QueryRowContext(ctx, `SELECT `+realmColumns+` FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.id=$1 AND r.deleted_at IS NULL AND NOT a.disabled`, id))
	if e != nil {
		return social.Realm{}, e
	}
	if e = canViewRealm(ctx, tx, v, viewer); e != nil {
		return social.Realm{}, e
	}
	return v, nil
}
