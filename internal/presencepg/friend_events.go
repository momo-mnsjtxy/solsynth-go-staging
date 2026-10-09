// SPDX-License-Identifier: AGPL-3.0-only
package presencepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/ports/passport/presence"
	"time"
)

func (r *Repository) FriendStatusEventInTransaction(ctx context.Context, tx *sql.Tx, viewer string, source friendeventpg.Source) (json.RawMessage, bool, error) {
	var now time.Time
	if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return nil, false, e
	}
	p, e := prefs(ctx, tx, source.Owner)
	if e != nil {
		return nil, false, e
	}
	if p.StatusVisibility == "private" {
		return nil, false, nil
	}
	status, _, e := FriendStatusInTransaction(ctx, tx, viewer, source.Owner, now)
	if errors.Is(e, presence.ErrNotFound) {
		return nil, false, nil
	}
	if e != nil || status.AccountID == "" {
		return nil, false, e
	}
	// The pinned first-party listener consumes account_id/status and ignores
	// device_id. Omission is deliberate: a credential ID is not a device ID.
	data, e := json.Marshal(map[string]any{"account_id": source.Owner, "status": status, "timestamp": source.At})
	return data, e == nil, e
}
