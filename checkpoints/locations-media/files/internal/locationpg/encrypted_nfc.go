// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"io"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
)

func (r *Repository) WithEncryptedNFC(key []byte, locker locations.NFCLocker) error {
	if len(key) == 0 {
		r.nfcLocker = locker
		return nil
	}
	if len(key) != 32 {
		return locations.ErrInvalid
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return e
	}
	r.nfcKeys, e = cipher.NewGCM(b)
	r.nfcLocker = locker
	return e
}
func (r *Repository) sealNFC(id string, key []byte) ([]byte, error) {
	if r.nfcKeys == nil {
		return nil, locations.ErrUnavailable
	}
	nonce := make([]byte, r.nfcKeys.NonceSize())
	if _, e := io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return r.nfcKeys.Seal(nonce, nonce, key, []byte(id)), nil
}
func (r *Repository) openNFC(id string, v []byte) ([]byte, error) {
	if r.nfcKeys == nil || len(v) < r.nfcKeys.NonceSize() {
		return nil, locations.ErrUnavailable
	}
	n := r.nfcKeys.NonceSize()
	b, e := r.nfcKeys.Open(nil, v[:n], v[n:], []byte(id))
	if e != nil {
		return nil, locations.ErrUnavailable
	}
	return b, nil
}
func parseNFCKey(s string) ([]byte, error) {
	b, e := hex.DecodeString(s)
	if e != nil || len(b) != 16 {
		b, e = base64.StdEncoding.Strict().DecodeString(s)
	}
	if e != nil || len(b) != 16 {
		return nil, locations.ErrInvalid
	}
	return b, nil
}
func lockNFCUID(ctx context.Context, tx *sql.Tx, s string) error {
	_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,19424))`, s)
	return e
}
func (r *Repository) ProvisionEncryptedTag(ctx context.Context, actor string, v locations.ProvisionTag) (locations.EncryptedTag, error) {
	var out locations.EncryptedTag
	normalized, e := locations.NormalizeUID(v.UID)
	if e != nil || len(normalized) != 14 || v.AssignedUserID != nil && !authority.ValidID(*v.AssignedUserID) {
		return out, locations.ErrInvalid
	}
	key, e := parseNFCKey(v.SunKey)
	if e != nil {
		return out, e
	}
	meta := key
	if v.MetaKey != "" {
		meta, e = parseNFCKey(v.MetaKey)
		if e != nil {
			return out, e
		}
	}
	if v.MACProfile == "" {
		v.MACProfile = "empty"
	}
	if v.MACProfile != "empty" && v.MACProfile != "encrypted-file" {
		return out, locations.ErrInvalid
	}
	if r.nfcKeys == nil {
		return out, locations.ErrUnavailable
	}
	tx, p, e := r.begin(ctx, actor, "nfc.admin.manage", true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if r.guard == nil && !p.Account.Superuser {
		return out, locations.ErrForbidden
	}
	if v.AssignedUserID != nil {
		if _, e = account(ctx, tx, *v.AssignedUserID); e != nil {
			return out, e
		}
	}
	if e = lockNFCUID(ctx, tx, normalized); e != nil {
		return out, e
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_nfc_tags WHERE uid=$1) OR EXISTS(SELECT 1 FROM owned_encrypted_nfc_tags WHERE uid=$1)`, normalized).Scan(&exists); e != nil {
		return out, e
	}
	if exists {
		return out, locations.ErrConflict
	}
	id, e := uid()
	if e != nil {
		return out, e
	}
	wrapped, e := r.sealNFC(id, append(append([]byte{}, meta...), key...))
	if e != nil {
		return out, e
	}
	now := r.now().UTC()
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_encrypted_nfc_tags(id,uid,account_id,key_ciphertext,mac_profile,is_active,counter,created_at,updated_at) VALUES($1,$2,$3,$4,$5,true,-1,$6,$6)`, id, normalized, v.AssignedUserID, wrapped, v.MACProfile, now)
	if e != nil {
		return out, mapped(e)
	}
	if e = audit(ctx, tx, actor, id, "nfc.encrypted.provision", now); e != nil {
		return out, e
	}
	if e = r.recheck(ctx, tx, actor, "nfc.admin.manage"); e != nil {
		return out, e
	}
	out = locations.EncryptedTag{Tag: locations.Tag{ID: id, UID: normalized, Active: true, Encrypted: true, CreatedAt: now}, AccountID: v.AssignedUserID}
	return out, tx.Commit()
}

const encryptedFields = `id::text,uid,account_id::text,label,is_active,locked_at IS NOT NULL,last_seen_at,created_at`

func encryptedTag(row interface{ Scan(...any) error }) (v locations.EncryptedTag, e error) {
	e = row.Scan(&v.ID, &v.UID, &v.AccountID, &v.Label, &v.Active, &v.Locked, &v.LastSeenAt, &v.CreatedAt)
	v.Encrypted = true
	return v, mapped(e)
}
func (r *Repository) encryptedTags(ctx context.Context, actor string, admin bool) ([]locations.EncryptedTag, error) {
	permission := ""
	if admin {
		permission = "nfc.admin.manage"
	}
	tx, p, e := r.begin(ctx, actor, permission, true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if admin && r.guard == nil && !p.Account.Superuser {
		return nil, locations.ErrForbidden
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+encryptedFields+` FROM owned_encrypted_nfc_tags WHERE deleted_at IS NULL AND ($1 OR account_id=$2::uuid) ORDER BY created_at DESC,id LIMIT 1000`, admin, actor)
	if e != nil {
		return nil, e
	}
	out := []locations.EncryptedTag{}
	for rows.Next() {
		v, e := encryptedTag(rows)
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
	if e = r.recheck(ctx, tx, actor, permission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) EncryptedTags(ctx context.Context, actor string) ([]locations.EncryptedTag, error) {
	return r.encryptedTags(ctx, actor, true)
}
func (r *Repository) ResolveSUN(ctx context.Context, actor string, v locations.SUNRequest, claim bool) (locations.TagResolution, error) {
	var out locations.TagResolution
	if !authority.ValidID(v.RecordID) {
		return out, locations.ErrInvalid
	}
	permission := "nfc.tags.read"
	if claim {
		permission = "nfc.tags.claim"
	}
	tx, _, e := r.begin(ctx, actor, permission, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var encrypted []byte
	var owner *string
	var tagUID, profile string
	var counter int
	e = tx.QueryRowContext(ctx, `SELECT uid,account_id::text,key_ciphertext,mac_profile,counter FROM owned_encrypted_nfc_tags WHERE id=$1 AND is_active AND deleted_at IS NULL FOR UPDATE`, v.RecordID).Scan(&tagUID, &owner, &encrypted, &profile, &counter)
	if e != nil {
		return out, mapped(e)
	}
	key, e := r.openNFC(v.RecordID, encrypted)
	if e != nil || len(key) != 32 {
		return out, locations.ErrUnavailable
	}
	read, e := verifySUN(key[:16], key[16:], tagUID, v.PICCData, v.Encrypted, v.CMAC, profile)
	clear(key)
	if e != nil {
		return out, e
	}
	if read <= counter {
		return out, locations.ErrConflict
	}
	if claim {
		if owner != nil && *owner != actor {
			return out, locations.ErrNotFound
		}
		owner = &actor
	} else if owner == nil {
		out = locations.TagResolution{ID: v.RecordID, Claimed: false, Actions: []string{"claim"}}
	}
	if owner != nil {
		blocked, friend, e := socialState(ctx, tx, actor, *owner)
		if e != nil {
			return out, e
		}
		if blocked {
			return out, locations.ErrNotFound
		}
		a, e := account(ctx, tx, *owner)
		if e != nil {
			return out, e
		}
		out = locations.TagResolution{ID: v.RecordID, Account: a, Friend: friend && actor != *owner, Claimed: true, Actions: []string{"view_profile"}}
		if actor != *owner && !friend {
			out.Actions = append(out.Actions, "add_friend")
		}
	}
	if e = r.recheck(ctx, tx, actor, permission); e != nil {
		return out, e
	}
	// Rebuild the owner projection after all credential and tag-row waits.
	// Holding the caller's account does not prevent the other account blocking it.
	if owner != nil {
		blocked, friend, e := socialState(ctx, tx, actor, *owner)
		if e != nil {
			return out, e
		}
		if blocked {
			return out, locations.ErrNotFound
		}
		a, e := account(ctx, tx, *owner)
		if e != nil {
			return out, e
		}
		out.Account = a
		out.Friend = friend && actor != *owner
		out.Actions = []string{"view_profile"}
		if actor != *owner && !friend {
			out.Actions = append(out.Actions, "add_friend")
		}
	}
	now := r.now().UTC()
	_, e = tx.ExecContext(ctx, `UPDATE owned_encrypted_nfc_tags SET account_id=$2,counter=$3,last_seen_at=$4,updated_at=$4 WHERE id=$1`, v.RecordID, owner, read, now)
	if e != nil {
		return out, e
	}
	if e = audit(ctx, tx, actor, v.RecordID, "nfc.sun.verify", now); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) ChangeEncryptedTag(ctx context.Context, actor, id string, v locations.TagPatch, remove bool) (locations.Tag, error) {
	if !authority.ValidID(id) || !locations.Text(v.Label, 64) {
		return locations.Tag{}, locations.ErrInvalid
	}
	permission := "nfc.tags.update"
	if remove {
		permission = "nfc.tags.delete"
	}
	tx, _, e := r.begin(ctx, actor, permission, true)
	if e != nil {
		return locations.Tag{}, e
	}
	defer tx.Rollback()
	vtag, e := encryptedTag(tx.QueryRowContext(ctx, `SELECT `+encryptedFields+` FROM owned_encrypted_nfc_tags WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, actor))
	if e != nil {
		return locations.Tag{}, e
	}
	if v.Label != nil {
		vtag.Label = v.Label
	}
	if v.Active != nil {
		vtag.Active = *v.Active
	}
	if remove {
		vtag.Active = false
	}
	if e = r.recheck(ctx, tx, actor, permission); e != nil {
		return locations.Tag{}, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_encrypted_nfc_tags SET label=$2,is_active=$3,deleted_at=CASE WHEN $4 THEN clock_timestamp() ELSE deleted_at END,updated_at=clock_timestamp() WHERE id=$1`, id, vtag.Label, vtag.Active, remove)
	if e != nil {
		return locations.Tag{}, e
	}
	if e = audit(ctx, tx, actor, id, "nfc.encrypted.update", r.now().UTC()); e != nil {
		return locations.Tag{}, e
	}
	return vtag.Tag, tx.Commit()
}
func (r *Repository) LockEncryptedTag(ctx context.Context, actor, id string) (locations.Tag, error) {
	if !authority.ValidID(id) {
		return locations.Tag{}, locations.ErrInvalid
	}
	tx, _, e := r.begin(ctx, actor, "nfc.tags.lock", true)
	if e != nil {
		return locations.Tag{}, e
	}
	defer tx.Rollback()
	v, e := encryptedTag(tx.QueryRowContext(ctx, `SELECT `+encryptedFields+` FROM owned_encrypted_nfc_tags WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, actor))
	if e != nil {
		return locations.Tag{}, e
	}
	if v.Locked {
		return v.Tag, tx.Commit()
	}
	if !v.Active || r.nfcLocker == nil {
		return locations.Tag{}, locations.ErrUnavailable
	}
	if e = r.nfcLocker.Lock(ctx, id, v.UID); e != nil {
		return locations.Tag{}, locations.ErrUnavailable
	}
	if e = r.recheck(ctx, tx, actor, "nfc.tags.lock"); e != nil {
		return locations.Tag{}, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_encrypted_nfc_tags SET locked_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, id); e != nil {
		return locations.Tag{}, e
	}
	if e = audit(ctx, tx, actor, id, "nfc.hardware.lock", r.now().UTC()); e != nil {
		return locations.Tag{}, e
	}
	v.Locked = true
	return v.Tag, tx.Commit()
}

var _ locations.EncryptedNFCRepository = (*Repository)(nil)
