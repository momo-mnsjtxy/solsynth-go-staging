// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"strings"
	"time"
)

const bundleFields = `id::text,account_id::text,name,description,expired_at,is_public,passcode_hash IS NOT NULL,created_at,updated_at`

func scanBundle(row interface{ Scan(...any) error }) (out media.Bundle, e error) {
	e = row.Scan(&out.ID, &out.AccountID, &out.Name, &out.Description, &out.ExpiredAt, &out.Public, &out.Protected, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = media.ErrNotFound
	}
	out.Files = []media.File{}
	return out, e
}
func passcodeDigest(pass string, salt []byte) ([]byte, error) {
	if len(pass) < 1 || len(pass) > 256 || strings.ContainsRune(pass, 0) {
		return nil, media.ErrInvalid
	}
	return pbkdf2.Key(sha256.New, pass, salt, 600000, 32)
}
func (r *Repository) SaveBundle(ctx context.Context, actor, id string, v media.BundleRequest) (media.Bundle, error) {
	var out media.Bundle
	if len(v.Name) > 255 || len(v.Description) > 8192 || strings.ContainsRune(v.Name, 0) || strings.ContainsRune(v.Description, 0) || v.ExpiredAt != nil && (!v.ExpiredAt.After(time.Now()) || v.ExpiredAt.After(time.Now().Add(366*24*time.Hour))) {
		return out, media.ErrInvalid
	}
	permission := media.CreatePermission
	if id != "" {
		if !authority.ValidID(id) {
			return out, media.ErrInvalid
		}
		permission = media.UpdatePermission
	}
	tx, e := r.begin(ctx, actor, permission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var salt, hash []byte
	if id != "" {
		var owner string
		e = tx.QueryRowContext(ctx, `SELECT account_id::text,passcode_salt,passcode_hash FROM owned_media_bundles WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, actor).Scan(&owner, &salt, &hash)
		if errors.Is(e, sql.ErrNoRows) {
			return out, media.ErrNotFound
		}
		if e != nil {
			return out, e
		}
		if owner != actor {
			return out, media.ErrNotFound
		}
	} else {
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_media_bundles WHERE account_id=$1 AND deleted_at IS NULL`, actor).Scan(&n); e != nil {
			return out, e
		}
		if n >= 1000 {
			return out, media.ErrQuota
		}
		id, e = uuid()
		if e != nil {
			return out, e
		}
	}
	if v.Passcode != nil {
		salt = nil
		hash = nil
		if *v.Passcode != "" {
			salt = make([]byte, 16)
			if _, e = rand.Read(salt); e != nil {
				return out, e
			}
			hash, e = passcodeDigest(*v.Passcode, salt)
			if e != nil {
				return out, e
			}
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
		return out, e
	}
	out, e = scanBundle(tx.QueryRowContext(ctx, `INSERT INTO owned_media_bundles(id,account_id,name,description,is_public,passcode_salt,passcode_hash,expired_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp(),clock_timestamp()) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,is_public=EXCLUDED.is_public,passcode_salt=EXCLUDED.passcode_salt,passcode_hash=EXCLUDED.passcode_hash,expired_at=EXCLUDED.expired_at,updated_at=clock_timestamp() RETURNING `+bundleFields, id, actor, v.Name, v.Description, v.Public, salt, hash, v.ExpiredAt))
	if e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func bundlePermission(ctx context.Context, tx *sql.Tx, actor, id, pass string) (bool, error) {
	var owner string
	var public bool
	var salt, hash []byte
	e := tx.QueryRowContext(ctx, `SELECT b.account_id::text,b.is_public,b.passcode_salt,b.passcode_hash FROM owned_media_bundles b JOIN owned_authority_accounts a ON a.id=b.account_id WHERE b.id=$1 AND b.deleted_at IS NULL AND (b.expired_at IS NULL OR b.expired_at>clock_timestamp()) AND NOT a.disabled`, id).Scan(&owner, &public, &salt, &hash)
	if errors.Is(e, sql.ErrNoRows) {
		return false, media.ErrNotFound
	}
	if e != nil {
		return false, e
	}
	if owner == actor {
		return true, nil
	}
	if !public {
		return false, media.ErrNotFound
	}
	if len(hash) == 0 {
		return true, nil
	}
	if actor == "" {
		return false, authority.ErrInvalid
	}
	verified, ok := ctx.Value(verifiedBundleKey{}).(verifiedBundle)
	if !ok || verified.id != id || subtle.ConstantTimeCompare(hash, verified.hash) != 1 || subtle.ConstantTimeCompare(salt, verified.salt) != 1 {
		return false, media.ErrForbidden
	}
	return true, nil
}
func (r *Repository) bundleAudience(ctx context.Context, tx *sql.Tx, actor, file string) (bool, error) {
	var id *string
	e := tx.QueryRowContext(ctx, `SELECT bundle_id::text FROM owned_media_details WHERE file_id=$1`, file).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) || e == nil && id == nil {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return bundlePermission(ctx, tx, actor, *id, media.BundlePasscode(ctx))
}
func (r *Repository) Bundle(ctx context.Context, actor, id, pass string) (media.Bundle, error) {
	var out media.Bundle
	if !authority.ValidID(id) {
		return out, media.ErrInvalid
	}
	ctx, e := r.prepareBundleAccess(ctx, actor, id, "", pass)
	if e != nil {
		return out, e
	}
	var tx *sql.Tx
	if actor != "" {
		tx, e = r.begin(ctx, actor, media.ReadPermission)
	} else {
		tx, e = r.db.BeginTx(ctx, nil)
	}
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = bundlePermission(ctx, tx, actor, id, pass); e != nil {
		return out, e
	}
	out, e = scanBundle(tx.QueryRowContext(ctx, `SELECT `+bundleFields+` FROM owned_media_bundles WHERE id=$1`, id))
	if e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage FROM owned_media_files f JOIN owned_media_details d ON d.file_id=f.id JOIN owned_authority_accounts a ON a.id=f.account_id WHERE d.bundle_id=$1 AND f.state='ready' AND NOT a.disabled AND (d.expired_at IS NULL OR d.expired_at>clock_timestamp()) ORDER BY f.id LIMIT 1000`, id)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		f, e := scan(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Files = append(out.Files, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if _, e = bundlePermission(ctx, tx, actor, id, pass); e != nil {
		return out, e
	}
	if actor != "" {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
			return out, e
		}
	}
	return out, tx.Commit()
}
func (r *Repository) Bundles(ctx context.Context, actor string, take, offset int) ([]media.Bundle, error) {
	if take < 1 || take > 100 || offset < 0 || offset > 100000 {
		return nil, media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+bundleFields+` FROM owned_media_bundles WHERE account_id=$1 AND deleted_at IS NULL ORDER BY created_at DESC,id LIMIT $2 OFFSET $3`, actor, take, offset)
	if e != nil {
		return nil, e
	}
	out := []media.Bundle{}
	for rows.Next() {
		v, e := scanBundle(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) DeleteBundle(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.DeletePermission)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result, e := tx.ExecContext(ctx, `UPDATE owned_media_bundles SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, actor)
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
