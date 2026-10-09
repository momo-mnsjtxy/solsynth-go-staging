// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
	"io"
	"solsynth.local/go-migration/ports/media"
)

// ReserveParentUpload uses the canonical durable pending reservation. It creates
// only private media and chooses a fresh opaque key. Cleanup may be called only
// after the parent's transaction ends: it cancels this reservation while pending,
// never a ready object from a successful or uncertain parent commit. Crashes leave
// the normal pending TTL/cleanup record and reserved quota, not untracked bytes.
func (r *Repository) ReserveParentUpload(ctx context.Context, actor string, u media.Upload) (media.File, func(context.Context) error, error) {
	if r == nil || r.db == nil || r.store == nil || r.guard == nil {
		return media.File{}, nil, media.ErrUnavailable
	}
	key, err := uuid()
	if err != nil {
		return media.File{}, nil, err
	}
	u.IdempotencyKey = "parent:" + key
	u.Public = false
	if err = validUpload(u, r.cfg.MaxFileBytes); err != nil {
		return media.File{}, nil, err
	}
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return media.File{}, nil, err
	}
	defer tx.Rollback()
	f, replayed, err := r.reserveTx(ctx, tx, actor, u)
	if err != nil {
		return media.File{}, nil, err
	}
	if replayed {
		return media.File{}, nil, media.ErrConflict
	}
	// Quota contention can outlive the original proof even while the account
	// lock excludes revocation. Never persist a reservation on stale authority.
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); err != nil {
		return media.File{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		// An uncertain commit still has the canonical pending TTL for recovery.
		return media.File{}, nil, err
	}
	objectID := f.ID
	cleanup := func(cleanCtx context.Context) error {
		// Only this generated, private, still-pending reservation can be cancelled.
		// A ready row must survive uncertain commit reporting and later consumers.
		result, e := r.db.ExecContext(cleanCtx, `UPDATE owned_media_files SET state='deleting',updated_at=clock_timestamp() WHERE id=$1 AND account_id=$2 AND idempotency_key=$3 AND state='pending'`, objectID, actor, u.IdempotencyKey)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		_, e = r.cleanupOne(cleanCtx, objectID, false)
		return e
	}
	return f, cleanup, nil
}

// FinishParentUpload makes the reserved bytes ready in the caller's canonical
// authority transaction. Parent association, audit, quota and ready-state commit
// together. On rollback the durable reservation remains eligible for cleanup.
func (r *Repository) FinishParentUpload(ctx context.Context, tx *sql.Tx, actor string, f media.File, body io.Reader) (media.File, error) {
	if r == nil || r.db == nil || r.store == nil || r.guard == nil || tx == nil || body == nil {
		return media.File{}, media.ErrUnavailable
	}
	if f.AccountID != actor || f.Public || f.State != "pending" {
		return media.File{}, media.ErrInvalid
	}
	if _, err := r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); err != nil {
		return f, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor); err != nil {
		return f, err
	}
	current, err := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1 AND account_id=$2 AND state='pending' AND NOT is_public AND expires_at>clock_timestamp() FOR UPDATE`, f.ID, actor))
	if err != nil {
		return f, err
	}
	if current.Size != f.Size || current.SHA256 != f.SHA256 || current.Name != f.Name || current.MIMEType != f.MIMEType {
		return f, media.ErrConflict
	}
	// Do not start object storage on a proof that expired during row-lock waits.
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); err != nil {
		return f, err
	}
	if err = r.store.Put(ctx, f.ID, body, f.Size, f.SHA256); err != nil {
		return f, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); err != nil {
		return f, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_quotas SET reserved_bytes=reserved_bytes-$2,used_bytes=used_bytes+$2 WHERE account_id=$1`, actor, f.Size); err != nil {
		return f, err
	}
	return scan(tx.QueryRowContext(ctx, `UPDATE owned_media_files SET state='ready',charge='used',updated_at=clock_timestamp() WHERE id=$1 RETURNING `+fields, f.ID))
}
