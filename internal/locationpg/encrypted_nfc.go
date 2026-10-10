// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
	"time"
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
	defer clear(key)
	meta := key
	if v.MetaKey != "" {
		meta, e = parseNFCKey(v.MetaKey)
		if e != nil {
			return out, e
		}
	}
	defer clear(meta)
	if v.MACProfile == "" {
		v.MACProfile = "empty"
	}
	if v.MACProfile != "empty" && v.MACProfile != "encrypted-file" {
		return out, locations.ErrInvalid
	}
	if r.nfcKeys == nil {
		return out, locations.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	// No actor-first lock: administration may assign another account. Preflight
	// establishes the original permission, then acquire the complete sorted set.
	if r.guard != nil {
		if _, e = r.guard.Preflight(ctx, r.db, tx, actor, "nfc.admin.manage"); e != nil {
			return out, e
		}
	}
	target := actor
	if v.AssignedUserID != nil {
		target = *v.AssignedUserID
	}
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id IN($1,$2) ORDER BY id FOR UPDATE`, actor, target)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if r.guard == nil {
		var admin bool
		if e = tx.QueryRowContext(ctx, `SELECT is_superuser FROM owned_authority_accounts WHERE id=$1 AND NOT disabled`, actor).Scan(&admin); e != nil || !admin {
			return out, locations.ErrForbidden
		}
	}
	if e = r.recheck(ctx, tx, actor, "nfc.admin.manage"); e != nil {
		return out, e
	}
	if _, e = account(ctx, tx, target); e != nil {
		return out, e
	}
	if e = lockNFCUID(ctx, tx, normalized); e != nil {
		return out, e
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_nfc_tags WHERE uid=$1 AND deleted_at IS NULL) OR EXISTS(SELECT 1 FROM owned_encrypted_nfc_tags WHERE uid=$1 AND deleted_at IS NULL)`, normalized).Scan(&exists); e != nil {
		return out, e
	}
	if exists {
		return out, locations.ErrConflict
	}
	// Reuse an encrypted tombstone's identity and preserve counter/physical lock.
	// A deleted tag must neither become permanently unprovisionable nor accept an
	// earlier SUN again merely because its metadata was restored.
	var id string
	var oldKeys []byte
	var oldProfile string
	var physicallyLocked bool
	e = tx.QueryRowContext(ctx, `SELECT id::text,key_ciphertext,mac_profile,locked_at IS NOT NULL FROM owned_encrypted_nfc_tags WHERE uid=$1 AND deleted_at IS NOT NULL FOR UPDATE`, normalized).Scan(&id, &oldKeys, &oldProfile, &physicallyLocked)
	if errors.Is(e, sql.ErrNoRows) {
		id, e = uid()
	}
	if e != nil {
		return out, e
	}
	material := append(append([]byte{}, meta...), key...)
	if physicallyLocked {
		old, e := r.openNFC(id, oldKeys)
		equal := e == nil && subtle.ConstantTimeCompare(old, material) == 1 && oldProfile == v.MACProfile
		clear(old)
		if !equal {
			clear(material)
			return out, locations.ErrConflict
		}
	}

	wrapped, e := r.sealNFC(id, material)
	clear(material)
	if e != nil {
		return out, e
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return out, e
	}
	out, e = encryptedTag(tx.QueryRowContext(ctx, `INSERT INTO owned_encrypted_nfc_tags(id,uid,account_id,key_ciphertext,mac_profile,is_active,counter,created_at,updated_at) VALUES($1,$2,$3,$4,$5,true,-1,$6,$6) ON CONFLICT(id) DO UPDATE SET account_id=EXCLUDED.account_id,key_ciphertext=EXCLUDED.key_ciphertext,mac_profile=EXCLUDED.mac_profile,is_active=true,deleted_at=NULL,updated_at=EXCLUDED.updated_at RETURNING `+encryptedFields, id, normalized, v.AssignedUserID, wrapped, v.MACProfile, now))
	if e != nil {
		return out, mapped(e)
	}
	if e = audit(ctx, tx, actor, id, "nfc.encrypted.provision", now); e != nil {
		return out, e
	}
	if _, e = account(ctx, tx, target); e != nil {
		return out, e
	}
	if e = r.recheck(ctx, tx, actor, "nfc.admin.manage"); e != nil {
		return out, e
	}
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
	if owner != nil {
		blocked, friend, e := socialState(ctx, tx, actor, *owner)
		if e != nil {
			return locations.TagResolution{}, e
		}
		if blocked {
			return locations.TagResolution{}, locations.ErrNotFound
		}
		a, e := account(ctx, tx, *owner)
		if e != nil {
			return locations.TagResolution{}, e
		}
		out.Account = a
		out.Friend = friend && actor != *owner
		out.Actions = []string{"view_profile"}
		if actor != *owner && !friend {
			out.Actions = append(out.Actions, "add_friend")
		}
	}
	if e = r.recheck(ctx, tx, actor, permission); e != nil {
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
	if e = r.recheck(ctx, tx, actor, permission); e != nil {
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
		if e = r.recheck(ctx, tx, actor, "nfc.tags.lock"); e != nil {
			return locations.Tag{}, e
		}
		return v.Tag, tx.Commit()
	}
	if !v.Active || r.nfcLocker == nil {
		return locations.Tag{}, locations.ErrUnavailable
	}
	var encrypted []byte
	var profile string
	var counter int
	if e = tx.QueryRowContext(ctx, `SELECT key_ciphertext,mac_profile,counter FROM owned_encrypted_nfc_tags WHERE id=$1`, id).Scan(&encrypted, &profile, &counter); e != nil {
		return locations.Tag{}, e
	}
	key, e := r.openNFC(id, encrypted)
	if e != nil || len(key) != 32 {
		return locations.Tag{}, locations.ErrUnavailable
	}
	defer clear(key)
	proof, e := r.nfcLocker.Lock(ctx, locations.NFCLockRequest{RecordID: id, UID: v.UID, MetaKey: key[:16], FileKey: key[16:], MACProfile: profile, MinimumCounter: counter})
	if e != nil {
		return locations.Tag{}, locations.ErrUnavailable
	}
	digest, e := hex.DecodeString(proof.TranscriptSHA256)
	if e != nil || len(digest) != 32 || proof.SUN.RecordID != id {
		return locations.Tag{}, locations.ErrUnavailable
	}
	read, e := verifySUN(key[:16], key[16:], v.UID, proof.SUN.PICCData, proof.SUN.Encrypted, proof.SUN.CMAC, profile)
	if e != nil || read <= counter {
		return locations.Tag{}, locations.ErrUnavailable
	}
	if e = r.recheck(ctx, tx, actor, "nfc.tags.lock"); e != nil {
		return locations.Tag{}, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_encrypted_nfc_tags SET locked_at=clock_timestamp(),lock_proof_sha256=$2,counter=$3,last_seen_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, id, proof.TranscriptSHA256, read); e != nil {
		return locations.Tag{}, e
	}
	if e = audit(ctx, tx, actor, id, "nfc.hardware.lock", r.now().UTC()); e != nil {
		return locations.Tag{}, e
	}
	if e = r.recheck(ctx, tx, actor, "nfc.tags.lock"); e != nil {
		return locations.Tag{}, e
	}
	v.Locked = true
	return v.Tag, tx.Commit()
}

var _ locations.EncryptedNFCRepository = (*Repository)(nil)
