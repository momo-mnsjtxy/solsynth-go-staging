// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

func (r *Repository) ComposeLivePhoto(ctx context.Context, actor string, v media.LivePhotoRequest) (media.LivePhoto, error) {
	var out media.LivePhoto
	if !authority.ValidID(v.ImageID) || !authority.ValidID(v.VideoID) || v.ImageID == v.VideoID {
		return out, media.ErrInvalid
	}
	image, e := r.FileRecord(ctx, actor, v.ImageID)
	if e != nil {
		return out, e
	}
	video, e := r.FileRecord(ctx, actor, v.VideoID)
	if e != nil {
		return out, e
	}
	if image.EncryptionScheme != "" || video.EncryptionScheme != "" || image.BundleID != video.BundleID || image.PoolID != video.PoolID || video.MIMEType != "video/mp4" && video.MIMEType != "video/webm" {
		return out, media.ErrInvalid
	}
	// Both components are real, ready, owned files. The preview path validates
	// image pixels; an explicitly configured bounded decoder validates a video
	// frame rather than trusting uploaded MIME labels.
	_, thumb, e := r.Thumbnail(ctx, actor, image.ID)
	if e != nil {
		return out, e
	}
	thumb.Close()
	if r.renderer == nil {
		return out, media.ErrUnavailable
	}
	if video.Size > 20<<20 {
		return out, media.ErrTooLarge
	}
	_, body, e := r.Open(ctx, actor, video.ID)
	if e != nil {
		return out, e
	}
	data, e := io.ReadAll(io.LimitReader(body, (20<<20)+1))
	body.Close()
	if e != nil || len(data) > 20<<20 {
		return out, media.ErrTooLarge
	}
	if _, e = r.renderer.RenderPNG(ctx, video.MIMEType, data); e != nil {
		return out, e
	}
	tx, e := r.begin(ctx, actor, media.CreatePermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	// Reload the pair and its options after decoder waits, using the original
	// request proof. No parent/bundle grant can become attachment ownership.
	currentImage, e := r.fileRecordTx(ctx, tx, actor, image.ID)
	if e != nil {
		return out, e
	}
	currentVideo, e := r.fileRecordTx(ctx, tx, actor, video.ID)
	if e != nil {
		return out, e
	}
	if currentImage.SHA256 != image.SHA256 || currentVideo.SHA256 != video.SHA256 || currentImage.BundleID != currentVideo.BundleID || currentImage.PoolID != currentVideo.PoolID || currentImage.EncryptionScheme != "" || currentVideo.EncryptionScheme != "" {
		return out, media.ErrConflict
	}
	if _, e = r.bundleAudience(ctx, tx, actor, image.ID); e != nil {
		return out, e
	}
	id, e := uuid()
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); e != nil {
		return out, e
	}
	e = tx.QueryRowContext(ctx, `INSERT INTO owned_media_compositions(id,account_id,image_id,video_id,created_at) VALUES($1,$2,$3,$4,clock_timestamp()) ON CONFLICT(account_id,image_id,video_id) DO UPDATE SET deleted_at=NULL RETURNING id::text,created_at`, id, actor, image.ID, video.ID).Scan(&out.ID, &out.CreatedAt)
	if e != nil {
		return out, e
	}
	out.AccountID = actor
	out.ApplicationType = "live_photo"
	out.Image = currentImage.File
	out.Video = currentVideo.File
	return out, tx.Commit()
}

func (r *Repository) LivePhoto(ctx context.Context, actor, id string) (media.LivePhoto, error) {
	var out media.LivePhoto
	if !authority.ValidID(id) {
		return out, media.ErrInvalid
	}
	var image, video string
	e := r.db.QueryRowContext(ctx, `SELECT image_id::text,video_id::text FROM owned_media_compositions WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&image, &video)
	if errors.Is(e, sql.ErrNoRows) {
		return out, media.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	ctx, e = r.prepareBundleAccess(ctx, actor, "", image, media.BundlePasscode(ctx))
	if e != nil {
		return out, e
	}
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	e = tx.QueryRowContext(ctx, `SELECT c.id::text,c.account_id::text,c.image_id::text,c.video_id::text,c.created_at FROM owned_media_compositions c JOIN owned_authority_accounts a ON a.id=c.account_id WHERE c.id=$1 AND c.deleted_at IS NULL AND NOT a.disabled FOR SHARE OF c`, id).Scan(&out.ID, &out.AccountID, &image, &video, &out.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, media.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id IN($1,$2) AND state='ready' ORDER BY id FOR SHARE`, image, video)
	if e != nil {
		return out, e
	}
	var imageFile, videoFile media.File
	for rows.Next() {
		f, e := scan(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		if f.ID == image {
			imageFile = f
		} else {
			videoFile = f
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if imageFile.ID == "" || videoFile.ID == "" || imageFile.AccountID != out.AccountID || videoFile.AccountID != out.AccountID {
		return out, media.ErrNotFound
	}
	check := func(f media.File) error {
		bundleAllowed, e := r.bundleAudience(ctx, tx, actor, f.ID)
		if e != nil {
			return e
		}
		var expired bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_media_details d LEFT JOIN owned_media_bundles b ON b.id=d.bundle_id WHERE d.file_id=$1 AND(d.expired_at<=clock_timestamp() OR b.deleted_at IS NOT NULL OR b.expired_at<=clock_timestamp()))`, f.ID).Scan(&expired); e != nil {
			return e
		}
		if expired {
			return media.ErrNotFound
		}
		if e = artworkOwnerActive(ctx, tx, f.AccountID); e != nil {
			return e
		}
		if !f.Public && f.AccountID != actor && !bundleAllowed {
			allowed := false
			if r.readVisibility != nil {
				allowed, e = r.readVisibility.CanReadInTransaction(ctx, tx, actor, f.ID)
				if e != nil {
					return e
				}
			}
			if !allowed {
				return media.ErrNotFound
			}
		}
		return artworkOwnerActive(ctx, tx, f.AccountID)
	}
	// Both grants are checked in one transaction, and refreshed after policy waits.
	for i := 0; i < 2; i++ {
		if e = check(imageFile); e != nil {
			return out, e
		}
		if e = check(videoFile); e != nil {
			return out, e
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return out, e
	}
	if e = artworkOwnerActive(ctx, tx, out.AccountID); e != nil {
		return out, e
	}
	out.Image = imageFile
	out.Video = videoFile
	out.ApplicationType = "live_photo"
	return out, tx.Commit()
}
func (r *Repository) DeleteLivePhoto(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.DeletePermission)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result, e := tx.ExecContext(ctx, `UPDATE owned_media_compositions SET deleted_at=clock_timestamp() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, actor)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return media.ErrNotFound
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.DeletePermission); e != nil {
		return e
	}
	return tx.Commit()
}
