// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/sphere/locations"
)

func (r *Repository) WithMedia() *Repository { r.mediaEnabled = true; return r }
func (r *Repository) WithMediaProtection(protection mediapg.FileReadProtection) *Repository {
	r.mediaProtection = protection
	return r
}

const passiveImages = `('image/png','image/jpeg','image/gif','image/webp','image/avif','image/bmp')`

func (r *Repository) validateMeetImage(ctx context.Context, tx *sql.Tx, actor string, id *string) error {
	if id == nil {
		return nil
	}
	if !r.mediaEnabled {
		return locations.ErrUnavailable
	}
	var owner, mime, state string
	e := tx.QueryRowContext(ctx, `SELECT account_id::text,mime_type,state FROM owned_media_files WHERE id=$1 FOR SHARE`, *id).Scan(&owner, &mime, &state)
	if errors.Is(e, sql.ErrNoRows) {
		return locations.ErrNotFound
	}
	if e != nil {
		return e
	}
	if owner != actor {
		return locations.ErrForbidden
	}
	if state != "ready" {
		return locations.ErrNotFound
	}
	var expired bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_media_details d LEFT JOIN owned_media_bundles b ON b.id=d.bundle_id WHERE d.file_id=$1 AND(d.expired_at<=clock_timestamp() OR b.deleted_at IS NOT NULL OR b.expired_at<=clock_timestamp()))`, *id).Scan(&expired); e != nil {
		return e
	}
	if expired {
		return locations.ErrNotFound
	}
	if r.mediaProtection != nil {
		if e = r.mediaProtection.ProtectFileReadInTransaction(ctx, tx, actor, *id); e != nil {
			if errors.Is(e, media.ErrNotFound) {
				return locations.ErrNotFound
			}
			return e
		}
	}
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp":
		return nil
	}
	return locations.ErrInvalid
}
func (r *Repository) loadMeetImage(ctx context.Context, tx *sql.Tx, actor string, m *locations.Meet) error {
	m.Image = nil
	if m.ImageID == nil || !r.mediaEnabled {
		return nil
	}
	var f media.File
	e := tx.QueryRowContext(ctx, `SELECT f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage FROM owned_media_files f JOIN owned_meets m ON m.image_id=f.id AND m.host_id=f.account_id LEFT JOIN owned_media_details d ON d.file_id=f.id LEFT JOIN owned_media_bundles b ON b.id=d.bundle_id WHERE m.id=$1 AND m.deleted_at IS NULL AND f.state='ready' AND f.mime_type IN `+passiveImages+` AND(d.expired_at IS NULL OR d.expired_at>clock_timestamp()) AND(b.deleted_at IS NULL AND(b.expired_at IS NULL OR b.expired_at>clock_timestamp())) AND(b.id IS NULL OR f.account_id=$2 OR(b.is_public AND b.passcode_hash IS NULL))`, m.ID, actor).Scan(&f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if r.mediaProtection != nil {
		if e = r.mediaProtection.ProtectFileReadInTransaction(ctx, tx, actor, f.ID); e != nil {
			if errors.Is(e, media.ErrNotFound) {
				return nil
			}
			return e
		}
	}
	m.Image = f
	return nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return locations.ErrUnavailable
	}
	if r.nfcLocker != nil {
		if e := r.nfcLocker.Ready(ctx); e != nil {
			return e
		}
	}
	_, e := r.db.ExecContext(ctx, `SELECT image_id FROM owned_meets LIMIT 0;SELECT token_hash,session_id,valid_to FROM owned_nearby_tokens LIMIT 0;SELECT key_ciphertext,counter,locked_at,lock_proof_sha256 FROM owned_encrypted_nfc_tags LIMIT 0;SELECT account_id,last_at FROM owned_location_geocode_limits LIMIT 0`)
	return e
}
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	if !r.mediaEnabled || actor == "" {
		return false, nil
	}
	var allowed bool
	// Location cannot be asserted via a file-read request. Meet attachments grant
	// access only to the current participant or mutual-friend audience; nearby
	// discovery of a public meet never turns into an unbounded public-file grant.
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_meets m JOIN owned_media_files f ON f.id=m.image_id AND f.account_id=m.host_id JOIN owned_authority_accounts a ON a.id=m.host_id WHERE f.id=$2 AND f.state='ready' AND f.mime_type IN `+passiveImages+` AND m.deleted_at IS NULL AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=a.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp()) AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp())) AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships b WHERE b.status=-100 AND ((b.account_id=$1 AND b.related_id=m.host_id) OR (b.account_id=m.host_id AND b.related_id=$1))) AND (m.host_id=$1 OR EXISTS(SELECT 1 FROM owned_meet_participants p WHERE p.meet_id=m.id AND p.account_id=$1) OR (m.visibility<>1 AND EXISTS(SELECT 1 FROM owned_social_effective_relationships x WHERE x.account_id=$1 AND x.related_id=m.host_id AND x.status>=100) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships y WHERE y.account_id=m.host_id AND y.related_id=$1 AND y.status>=100))))`, actor, id).Scan(&allowed)
	return allowed, e
}
