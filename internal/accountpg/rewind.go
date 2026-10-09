// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// Trusted composition reads reuse canonical public/viewer projections and the
// caller's same-pool transaction. They do not authenticate an asserted viewer.
func (r *Repository) RewindAccount(ctx context.Context, db *sql.DB, tx *sql.Tx, id string) (accounts.Account, error) {
	if r == nil || r.db != db || tx == nil {
		return accounts.Account{}, accounts.ErrUnavailable
	}
	return lookupAccount(ctx, tx, id)
}
func (r *Repository) RewindPublisher(ctx context.Context, db *sql.DB, tx *sql.Tx, viewer, id string) (accounts.Publisher, error) {
	if r == nil || r.db != db || tx == nil {
		return accounts.Publisher{}, accounts.ErrUnavailable
	}
	return r.visiblePublisher(ctx, tx, viewer, id)
}
