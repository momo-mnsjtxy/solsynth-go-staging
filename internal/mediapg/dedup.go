// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/media"
)

// Dedupe shares only bytes already owned by this account and held in the same
// configured pool. Each logical file keeps its own access policy and quota
// charge. The freshly streamed bytes have already passed size/digest checks.
func (r *Repository) deduplicate(ctx context.Context, tx *sql.Tx, f media.File, store media.ObjectStore) error {
	var key string
	e := tx.QueryRowContext(ctx, `SELECT d.object_key::text FROM owned_media_files prior JOIN owned_media_details d ON d.file_id=prior.id JOIN owned_media_details incoming ON incoming.file_id=$1 WHERE prior.account_id=$2 AND prior.id<>$1 AND prior.state='ready' AND prior.sha256=$3 AND prior.size=$4 AND prior.mime_type=$5 AND d.pool_id IS NOT DISTINCT FROM incoming.pool_id AND COALESCE(d.options->>'encryption_scheme','')='' AND COALESCE(incoming.options->>'encryption_scheme','')='' AND (d.expired_at IS NULL OR d.expired_at>clock_timestamp()) ORDER BY prior.created_at,prior.id LIMIT 1`, f.ID, f.AccountID, f.SHA256, f.Size, f.MIMEType).Scan(&key)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	body, e := store.Open(ctx, key)
	if e != nil {
		return media.ErrUnavailable
	}
	body.Close()
	if e = store.Remove(ctx, f.ID); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_media_details SET object_key=$2 WHERE file_id=$1`, f.ID, key)
	return e
}
