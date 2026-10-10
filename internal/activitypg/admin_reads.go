// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/passport/activities"
	"time"
)

// The caller holds the target account lock and global accounts.view authority.
// Activity leases still require their original stored authentication eligibility.
func AdministrativeActivitiesInTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, g *authoritypg.TransactionGuard, id string) (out []activities.Activity, e error) {
	out = []activities.Activity{}
	rs, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE account_id=$1::uuid AND deleted_at IS NULL AND ended_at IS NULL AND lease_expires_at>clock_timestamp() ORDER BY created_at DESC,id LIMIT 1001`, id)
	if e != nil {
		return
	}
	all := []row{}
	for rs.Next() {
		v, x := scan(rs)
		if x != nil {
			rs.Close()
			return nil, x
		}
		all = append(all, v)
	}
	e = rs.Err()
	rs.Close()
	if e != nil {
		return
	}
	if len(all) > 1000 {
		return nil, activities.ErrUnavailable
	}
	for _, v := range all {
		ok, x := g.StoredAuthenticationEligible(ctx, db, tx, v.AccountID, v.session, v.version, v.epoch)
		if x != nil {
			return nil, x
		}
		if !ok {
			continue
		}
		if e = artwork(ctx, tx, id, &v.Content, false); e != nil {
			return nil, e
		}
		out = append(out, v.Activity)
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return nil, e
	}
	final := out[:0]
	for _, v := range out {
		if now.Before(v.LeaseExpiresAt) {
			final = append(final, v)
		}
	}
	out = final
	return
}
