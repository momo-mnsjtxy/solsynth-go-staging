// SPDX-License-Identifier: AGPL-3.0-only
package presencepg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/presence"
	"time"
)

// FriendStatusInTransaction uses the canonical projection after the caller has
// authenticated and locked viewer and owner. The recent fallback cannot reveal
// hidden status timestamps or invisibility through mere inclusion of an account.
// It has no authority effect and never accepts a name as an owner selector.
func FriendStatusInTransaction(ctx context.Context, tx *sql.Tx, viewer, owner string, now time.Time) (v presence.Status, recent bool, e error) {
	if tx == nil || !authority.ValidID(viewer) || !authority.ValidID(owner) || viewer == owner {
		return v, false, presence.ErrInvalid
	}
	v, e = projectStatus(ctx, tx, viewer, owner, now)
	if e != nil {
		return
	}
	var invisible bool
	e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT type=3 FROM owned_presence_statuses WHERE account_id=$1::uuid AND(cleared_at IS NULL OR cleared_at>$2::timestamptz) ORDER BY created_at DESC,id LIMIT 1),false)`, owner, now).Scan(&invisible)
	if e != nil {
		return
	}
	if invisible {
		return presence.Status{}, false, nil
	}
	// The caller separately requires a current accepted bilateral friendship.
	e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT status_visibility<>'private' FROM owned_presence_preferences WHERE account_id=$1::uuid),true) AND EXISTS(SELECT 1 FROM owned_presence_statuses WHERE account_id=$1::uuid AND updated_at>=$2::timestamptz-interval '24 hours' AND updated_at<=$2::timestamptz)`, owner, now).Scan(&recent)
	return
}
