// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

type verifiedBundleKey struct{}
type verifiedBundle struct {
	id         string
	salt, hash []byte
}

// Each expensive password computation consumes a durable authenticated attempt
// first. Failed reads cannot roll that attempt back. The short transaction ends
// before the file-read transaction, keeping the canonical account lock order.
// The cached result is scoped to this operation and the exact current password
// hash, so a password rotation cannot reuse a previously verified grant.
func (r *Repository) prepareBundleAccess(ctx context.Context, actor, bundle, file, pass string) (context.Context, error) {
	var id, owner string
	var public bool
	var salt, hash []byte
	query := `SELECT b.id::text,b.account_id::text,b.is_public,b.passcode_salt,b.passcode_hash FROM owned_media_bundles b JOIN owned_authority_accounts a ON a.id=b.account_id WHERE b.id=NULLIF($1,'')::uuid AND b.deleted_at IS NULL AND (b.expired_at IS NULL OR b.expired_at>clock_timestamp()) AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=a.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp()) AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))`
	if file != "" {
		query = `SELECT b.id::text,b.account_id::text,b.is_public,b.passcode_salt,b.passcode_hash FROM owned_media_details d JOIN owned_media_bundles b ON b.id=d.bundle_id JOIN owned_authority_accounts a ON a.id=b.account_id WHERE d.file_id=$1 AND b.deleted_at IS NULL AND (b.expired_at IS NULL OR b.expired_at>clock_timestamp()) AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=a.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp()) AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))`
		bundle = file
	}
	e := r.db.QueryRowContext(ctx, query, bundle).Scan(&id, &owner, &public, &salt, &hash)
	if errors.Is(e, sql.ErrNoRows) {
		return ctx, nil // The canonical read below decides absence/expiry/privacy.
	}
	if e != nil {
		return ctx, e
	}
	if owner == actor || !public || len(hash) == 0 {
		return ctx, nil
	}
	if verified, ok := ctx.Value(verifiedBundleKey{}).(verifiedBundle); ok && verified.id == id && subtle.ConstantTimeCompare(hash, verified.hash) == 1 && subtle.ConstantTimeCompare(salt, verified.salt) == 1 {
		return ctx, nil
	}
	if !authority.ValidID(actor) {
		return ctx, authority.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return ctx, e
	}
	defer tx.Rollback()
	var attempts int
	e = tx.QueryRowContext(ctx, `INSERT INTO owned_media_bundle_attempts(account_id,bundle_id,window_start,attempts) VALUES($1,$2,clock_timestamp(),1) ON CONFLICT(account_id,bundle_id) DO UPDATE SET window_start=CASE WHEN owned_media_bundle_attempts.window_start<=clock_timestamp()-interval '1 minute' THEN clock_timestamp() ELSE owned_media_bundle_attempts.window_start END,attempts=CASE WHEN owned_media_bundle_attempts.window_start<=clock_timestamp()-interval '1 minute' THEN 1 ELSE owned_media_bundle_attempts.attempts+1 END WHERE owned_media_bundle_attempts.window_start<=clock_timestamp()-interval '1 minute' OR owned_media_bundle_attempts.attempts<10 RETURNING attempts`, actor, id).Scan(&attempts)
	if errors.Is(e, sql.ErrNoRows) {
		return ctx, media.ErrRateLimit
	}
	if e != nil {
		return ctx, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return ctx, e
	}
	if e = tx.Commit(); e != nil {
		return ctx, e
	}
	candidate, e := passcodeDigest(pass, salt)
	if e != nil || subtle.ConstantTimeCompare(candidate, hash) != 1 {
		clear(candidate)
		return ctx, media.ErrForbidden
	}
	clear(candidate)
	return context.WithValue(ctx, verifiedBundleKey{}, verifiedBundle{id, salt, hash}), nil
}
