// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/media"
	"time"
)

// Call after the last domain wait as well as before writing an expiring grant.
// A request timestamp checked before acquiring a lock is not a current TTL.
func validateFutureExpiry(ctx context.Context, tx *sql.Tx, expiry *time.Time) error {
	if expiry == nil {
		return nil
	}
	var valid bool
	if e := tx.QueryRowContext(ctx, `SELECT $1::timestamptz>clock_timestamp() AND $1::timestamptz<=clock_timestamp()+interval '366 days'`, expiry).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return media.ErrInvalid
	}
	return nil
}
