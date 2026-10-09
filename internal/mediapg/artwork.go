// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/artwork"
)

//go:embed artwork_schema.sql
var ArtworkSchema string

func (r *Repository) ArtworkReady(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT hash,file_id,last_referenced_at,retention_seconds FROM owned_presence_artworks LIMIT 0`)
	return e
}
func artworkView(hash string, f media.File) artwork.Artwork {
	return artwork.Artwork{Hash: hash, MIMEType: f.MIMEType, Size: f.Size, URL: "/api/presence/artworks/" + hash}
}
func artworkLock(ctx context.Context, tx *sql.Tx, hash string) error {
	_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('presence-artwork:' || $1::text,0))`, hash)
	return e
}
func (r *Repository) artworkLive(ctx context.Context, tx *sql.Tx, hash, id string) (bool, error) {
	var live bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_presence_artworks WHERE hash=$1::text AND file_id=$2::uuid AND last_referenced_at>clock_timestamp()-(retention_seconds * interval '1 second'))`, hash, id).Scan(&live)
	return live, e
}

func artworkOwnerActive(ctx context.Context, tx *sql.Tx, actor string) error {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp()) AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE id=$1::uuid`, actor).Scan(&active)
	if errors.Is(e, sql.ErrNoRows) || e == nil && !active {
		return media.ErrNotFound
	}
	return e
}

// A full byte upload proves possession; a hash alone cannot create or move a
// canonical file. Global dedup never transfers the original uploader's ownership.
func (r *Repository) UploadArtwork(ctx context.Context, actor, typ string, data []byte) (artwork.Artwork, error) {
	var result artwork.Artwork
	if len(data) == 0 {
		return result, media.ErrInvalid
	}
	if len(data) > artwork.MaxBytes || int64(len(data)) > r.cfg.MaxFileBytes {
		return result, media.ErrTooLarge
	}
	typ, e := artwork.ImageType(typ)
	if e != nil {
		return result, e
	}
	digest := sha256.Sum256(data)
	hash := "sha256:" + hex.EncodeToString(digest[:])
	tx, e := r.begin(ctx, actor, media.CreatePermission, artwork.ManagePermission)
	if e != nil {
		return result, e
	}
	defer tx.Rollback()
	if e = artworkLock(ctx, tx, hash); e != nil {
		return result, e
	}
	var existingID string
	e = tx.QueryRowContext(ctx, `SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1::text`, hash).Scan(&existingID)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return result, e
	}
	if e == nil {
		f, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1::uuid FOR SHARE`, existingID))
		if e != nil {
			return result, e
		}
		if f.State == "pending" {
			return result, media.ErrPending
		}
		if f.State == "ready" {
			if f.SHA256 != strings.TrimPrefix(hash, "sha256:") || !strings.HasPrefix(f.MIMEType, "image/") {
				return result, media.ErrNotFound
			}
			if e = artworkOwnerActive(ctx, tx, f.AccountID); e != nil {
				return result, e
			}
			body, e := r.openFile(ctx, f)
			if e != nil {
				return result, e
			}
			body.Close()
			if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission, artwork.ManagePermission); e != nil {
				return result, e
			}
			// The owner can be a different account from the authenticated uploader;
			// re-read that policy after storage I/O before renewing the association.
			if e = artworkOwnerActive(ctx, tx, f.AccountID); e != nil {
				return result, e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp() WHERE hash=$1::text`, hash); e != nil {
				return result, e
			}
			return artworkView(hash, f), tx.Commit()
		}
	}
	key, e := uuid()
	if e != nil {
		return result, e
	}
	u := media.Upload{Name: "presence-artwork", MIMEType: typ, Size: int64(len(data)), SHA256: strings.TrimPrefix(hash, "sha256:"), IdempotencyKey: "artwork:" + key}
	f, _, e := r.reserveTx(ctx, tx, actor, u)
	if e != nil {
		return result, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_presence_artworks(hash,file_id,last_referenced_at,retention_seconds)VALUES($1::text,$2::uuid,clock_timestamp(),2592000) ON CONFLICT(hash)DO UPDATE SET file_id=EXCLUDED.file_id,last_referenced_at=EXCLUDED.last_referenced_at`, hash, f.ID); e != nil {
		return result, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission, artwork.ManagePermission); e != nil {
		return result, e
	}
	if e = tx.Commit(); e != nil {
		return result, e
	}
	f, e = r.finish(ctx, actor, f, bytes.NewReader(data), false, artwork.ManagePermission)
	if e != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, x := r.db.ExecContext(cleanup, `UPDATE owned_media_files SET state='deleting',updated_at=clock_timestamp() WHERE id=$1::uuid AND state='pending'`, f.ID); x == nil {
			_, _ = r.cleanupOne(cleanup, f.ID, false)
		}
		return result, e
	}
	return artworkView(hash, f), nil
}

func (r *Repository) OpenArtwork(ctx context.Context, actor, hash string) (artwork.Artwork, io.ReadSeekCloser, error) {
	var result artwork.Artwork
	hash, e := artwork.NormalizeHash(hash)
	if e != nil {
		return result, nil, e
	}
	var id string
	e = r.db.QueryRowContext(ctx, `SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1::text`, hash).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return result, nil, media.ErrNotFound
	}
	if e != nil {
		return result, nil, e
	}
	f, body, e := r.read(ctx, actor, id, true, hash)
	if e != nil {
		return result, nil, e
	}
	return artworkView(hash, f), body, nil
}

// Expiry is checked after every lock wait. The canonical deletion worker removes
// bytes before releasing quota, so crashes leave a durable retryable deletion.
func (r *Repository) CleanupArtworks(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, media.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT a.hash,a.file_id::text,f.account_id::text FROM owned_presence_artworks a JOIN owned_media_files f ON f.id=a.file_id WHERE a.last_referenced_at<=clock_timestamp()-(a.retention_seconds * interval '1 second') ORDER BY a.last_referenced_at,a.hash LIMIT $1::integer`, limit)
	if e != nil {
		return 0, e
	}
	type candidate struct{ hash, id, actor string }
	var list []candidate
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.hash, &c.id, &c.actor); e != nil {
			rows.Close()
			return 0, e
		}
		list = append(list, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	done := 0
	for _, c := range list {
		removed, e := r.expireArtwork(ctx, c.hash, c.id, c.actor)
		if e != nil {
			return done, e
		}
		if removed {
			done++
		}
	}
	return done, nil
}
func (r *Repository) expireArtwork(ctx context.Context, hash, id, actor string) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor); e != nil {
		return false, e
	}
	if e = artworkLock(ctx, tx, hash); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1::uuid FOR UPDATE`, actor); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_media_files WHERE id=$1::uuid FOR UPDATE`, id); e != nil {
		return false, e
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM owned_presence_artworks WHERE hash=$1::text AND file_id=$2::uuid AND last_referenced_at<=clock_timestamp()-(retention_seconds * interval '1 second')`, hash, id)
	if e != nil {
		return false, e
	}
	n, e := result.RowsAffected()
	if e != nil || n == 0 {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting',updated_at=clock_timestamp() WHERE id=$1::uuid AND state IN ('ready','pending')`, id); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	_, e = r.cleanupOne(ctx, id, false)
	return true, e
}
