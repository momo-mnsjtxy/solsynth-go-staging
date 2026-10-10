// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"solsynth.local/go-migration/ports/media"
)

// CleanupOwnedDeletions retries durable tombstones for one authenticated owner.
// It never selects another account's cleanup work from the shared worker queue.
func (r *Repository) CleanupOwnedDeletions(ctx context.Context, actor string, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, media.ErrInvalid
	}
	tx, err := r.begin(ctx, actor, media.DeletePermission)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id::text FROM owned_media_files WHERE account_id=$1 AND state='deleting' ORDER BY id LIMIT $2`, actor, limit)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, media.DeletePermission); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	done := 0
	for _, id := range ids {
		cleaned, err := r.cleanupOne(ctx, id, false)
		if err != nil {
			return done, err
		}
		if cleaned {
			done++
		}
	}
	return done, nil
}
