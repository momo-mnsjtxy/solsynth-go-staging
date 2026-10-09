// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// These projection helpers join a caller's already-authenticated transaction;
// they confer no authority and preserve the canonical current read predicates.
func (r *Repository) AccountInTransaction(ctx context.Context, tx *sql.Tx, viewer, ref string) (accounts.Account, error) {
	if tx == nil || !authority.ValidID(viewer) {
		return accounts.Account{}, accounts.ErrForbidden
	}
	return lookupAccount(ctx, tx, ref)
}
func (r *Repository) PublisherForViewerInTransaction(ctx context.Context, tx *sql.Tx, viewer, ref string) (accounts.Publisher, error) {
	if tx == nil || !authority.ValidID(viewer) {
		return accounts.Publisher{}, accounts.ErrForbidden
	}
	return r.visiblePublisher(ctx, tx, viewer, ref)
}
