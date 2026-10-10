// SPDX-License-Identifier: AGPL-3.0-only
package rewindpg

import (
	"context"
	"database/sql"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/rewind"
)

// CanonicalActionLogs measures current Go domain actions, not legacy remote
// telemetry. The explicit account-history view owns event attribution; Rewind
// neither copies events nor fabricates timestamps from last-login/profile data.
type CanonicalActionLogs struct{ db *sql.DB }

func NewCanonicalActionLogs(db *sql.DB) (*CanonicalActionLogs, error) {
	if db == nil {
		return nil, rewind.ErrUnavailable
	}
	return &CanonicalActionLogs{db: db}, nil
}
func (a *CanonicalActionLogs) Ready(ctx context.Context) error {
	if a == nil || a.db == nil {
		return rewind.ErrUnavailable
	}
	_, err := a.db.ExecContext(ctx, `SELECT account_id,id,source,source_id,created_at FROM owned_account_action_history LIMIT 0`)
	return err
}
func (a *CanonicalActionLogs) ActionTimes(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, start, end time.Time, limit int) ([]time.Time, error) {
	if a == nil || db != a.db || tx == nil || !authority.ValidID(actor) || !start.Before(end) || end.Sub(start) > 367*24*time.Hour || limit < 1 || limit > 100000 {
		return nil, rewind.ErrInvalid
	}
	// limit+1 detects overflow rather than silently turning a prefix into a report.
	rows, err := tx.QueryContext(ctx, `SELECT created_at FROM owned_account_action_history WHERE account_id=$1::uuid AND created_at>=$2::timestamptz AND created_at<$3::timestamptz ORDER BY created_at,id LIMIT $4::integer`, actor, start, end, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]time.Time, 0)
	for rows.Next() {
		var at time.Time
		if err = rows.Scan(&at); err != nil {
			return nil, err
		}
		if len(out) == limit {
			return nil, rewind.ErrUnavailable
		}
		out = append(out, at)
	}
	return out, rows.Err()
}
