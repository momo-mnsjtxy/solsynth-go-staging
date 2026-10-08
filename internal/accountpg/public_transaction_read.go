// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// PublicAccountInTransaction exposes exactly the already-public LookupAccount
// projection in a caller's transaction. It creates no viewer or authority.
func (r *Repository) PublicAccountInTransaction(ctx context.Context, tx *sql.Tx, ref string) (accounts.Account, error) {
	if r == nil || r.db == nil || tx == nil {
		return accounts.Account{}, accounts.ErrUnavailable
	}
	if e := ctx.Err(); e != nil {
		return accounts.Account{}, e
	}
	return lookupAccount(ctx, tx, ref)
}
