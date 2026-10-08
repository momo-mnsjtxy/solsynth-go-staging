// SPDX-License-Identifier: AGPL-3.0-only
package developpg

import (
	"database/sql"
	"solsynth.local/go-migration/internal/apppg"
)

// NewOwned reads and writes the canonical developer/app model. It never uses the
// retained legacy compatibility fixture tables or silently enables their policy.
func NewOwned(db *sql.DB, guard apppg.TransactionGuard) *apppg.Repository {
	return apppg.NewWithAuthority(db, guard)
}
