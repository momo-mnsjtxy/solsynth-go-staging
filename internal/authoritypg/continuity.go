// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
)

// ContinuityReady does not migrate. Hosts check this on their authority pool
// before accepting traffic after the explicit, atomic migration.
func (r *Repository) ContinuityReady(ctx context.Context) error {
	if r == nil || r.db == nil {
		return authority.ErrUnavailable
	}
	var ok bool
	e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('owned_authority_sessions') AND attname='authorization_epoch' AND attnotnull AND NOT attisdropped) AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass('owned_authority_sessions') AND conname='owned_authority_sessions_authorization_epoch_check' AND convalidated)`).Scan(&ok)
	if e != nil || !ok {
		return authority.ErrUnavailable
	}
	return nil
}
