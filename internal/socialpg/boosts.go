// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/passport/social"
)

// BoostProvider projects unexpired canonical paid contributions inside the
// caller's transaction. Configuration replaces manually stored boost levels.
type BoostProvider interface {
	BoostStateInTransaction(context.Context, *sql.Tx, string) (int64, int, error)
}

func (r *Repository) WithBoosts(p BoostProvider) *Repository { r.boosts = p; return r }
func (r *Repository) RefreshBoostInTransaction(ctx context.Context, tx *sql.Tx, v *social.Realm) error {
	if r.boosts == nil {
		return nil
	}
	points, level, e := r.boosts.BoostStateInTransaction(ctx, tx, v.ID)
	if e != nil {
		return e
	}
	v.BoostPoints = points
	v.BoostLevel = level
	return nil
}
