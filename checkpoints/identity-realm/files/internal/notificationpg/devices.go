// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed devices_schema.sql
var DevicesSchema string

type DeviceRewards interface {
	ApplyCanonicalEvent(context.Context, *sql.Tx, string, string, string) error
}
type DeviceConfig struct {
	EncryptionKey []byte
	Provider      notifications.PushProvider
}
type DeviceStore struct {
	db       *sql.DB
	guard    *authoritypg.TransactionGuard
	seal     cipher.AEAD
	provider notifications.PushProvider
	rewards  DeviceRewards
}

func NewDevices(db *sql.DB, g *authoritypg.TransactionGuard, c DeviceConfig, rewards DeviceRewards) (*DeviceStore, error) {
	if db == nil || g == nil || len(c.EncryptionKey) != 32 || c.Provider == nil || rewards == nil {
		return nil, notifications.ErrUnavailable
	}
	block, e := aes.NewCipher(c.EncryptionKey)
	if e != nil {
		return nil, e
	}
	seal, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &DeviceStore{db, g, seal, c.Provider, rewards}, nil
}
func (d *DeviceStore) Ready(ctx context.Context) error {
	_, e := d.db.ExecContext(ctx, `SELECT id,token_hash,sealed_token,session_id FROM notification_v1.devices LIMIT 0;SELECT code_hash,sent_at FROM notification_v1.device_challenges LIMIT 0`)
	return e
}
func (d *DeviceStore) begin(ctx context.Context, actor string) (tx *sql.Tx, p authority.Principal, e error) {
	if d == nil || !authority.ValidID(actor) {
		return nil, p, notifications.ErrInvalid
	}
	tx, e = d.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer func() {
		if e != nil {
			tx.Rollback()
		}
	}()
	if _, e = d.guard.Preflight(ctx, d.db, tx, actor, notifications.DevicePermission); e != nil {
		return
	}
	var id string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor).Scan(&id); e != nil {
		return
	}
	p, e = d.authorize(ctx, tx, actor)
	return
}
func (d *DeviceStore) authorize(ctx context.Context, tx *sql.Tx, actor string) (authority.Principal, error) {
	p, e := d.guard.Authorize(ctx, d.db, tx, actor, notifications.DevicePermission)
	if e == nil && (p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "") {
		e = authority.ErrForbidden
	}
	return p, e
}
func deviceToken(t string) bool {
	if len(t) < 16 || len(t) > 4096 {
		return false
	}
	for _, c := range t {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == ':') {
			return false
		}
	}
	return true
}
func (d *DeviceStore) sealToken(actor, id, token string) ([]byte, error) {
	nonce := make([]byte, d.seal.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return nil, e
	}
	return d.seal.Seal(nonce, nonce, []byte(token), []byte(actor+":"+id)), nil
}
func (d *DeviceStore) openToken(actor, id string, sealed []byte) (string, error) {
	if len(sealed) < d.seal.NonceSize() {
		return "", notifications.ErrUnavailable
	}
	raw, e := d.seal.Open(nil, sealed[:d.seal.NonceSize()], sealed[d.seal.NonceSize():], []byte(actor+":"+id))
	if e != nil {
		return "", notifications.ErrUnavailable
	}
	return string(raw), nil
}
func (d *DeviceStore) StartDevice(ctx context.Context, actor string, q notifications.DeviceRequest) (out notifications.DeviceChallenge, e error) {
	if !authority.ValidID(q.DeviceID) || !deviceToken(q.Token) || len(q.Label) > 128 || !utf8.ValidString(q.Label) || strings.ContainsRune(q.Label, 0) || q.ConsentHours < 1 || q.ConsentHours > 720 {
		return out, notifications.ErrInvalid
	}
	tx, p, e := d.begin(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	hash := sha256.Sum256([]byte("owned-fcm-token:" + q.Token))
	var owner string
	e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM notification_v1.devices WHERE id=$1::uuid OR token_hash=$2::bytea ORDER BY account_id LIMIT 1`, q.DeviceID, hash[:]).Scan(&owner)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return
	}
	if e == nil && owner != actor {
		return out, notifications.ErrConflict
	}
	e = nil
	var outstanding int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_v1.device_challenges WHERE account_id=$1::uuid AND created_at>clock_timestamp()-interval '10 minutes'`, actor).Scan(&outstanding); e != nil {
		return
	}
	if outstanding >= 8 {
		return out, notifications.ErrConflict
	}
	out.ID, e = uuid()
	if e != nil {
		return
	}
	code := make([]byte, 32)
	if _, e = rand.Read(code); e != nil {
		return
	}
	codeString := base64.RawURLEncoding.EncodeToString(code)
	digest := sha256.Sum256([]byte(codeString))
	sealed, e := d.sealToken(actor, q.DeviceID, q.Token)
	if e != nil {
		return
	}
	e = tx.QueryRowContext(ctx, `INSERT INTO notification_v1.device_challenges(id,account_id,device_id,label,token_hash,sealed_token,code_hash,session_id,account_version,authorization_epoch,consent_hours,created_at,expires_at)SELECT $1::uuid,$2::uuid,$3::uuid,$4::text,$5::bytea,$6::bytea,$7::bytea,$8::uuid,$9::bigint,$10::bigint,$11::integer,t.now,LEAST(t.now+interval '10 minutes',$12::timestamptz)FROM(SELECT clock_timestamp()now)t RETURNING expires_at`, out.ID, actor, q.DeviceID, q.Label, hash[:], sealed, digest[:], p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, q.ConsentHours, p.Session.ExpiresAt).Scan(&out.ExpiresAt)
	if e != nil {
		return
	}
	if _, e = d.authorize(ctx, tx, actor); e != nil {
		return
	}
	if e = tx.Commit(); e != nil {
		return
	}
	// The confirmation secret is delivered only to the token's real device.
	bounded, cancel := context.WithDeadline(ctx, out.ExpiresAt)
	defer cancel()
	if e = d.provider.SendDevice(bounded, q.Token, map[string]string{"kind": "device.challenge", "challenge_id": out.ID, "challenge": codeString}); e != nil {
		return out, notifications.ErrDeliveryUnconfigured
	}
	tx, after, e := d.begin(ctx, actor)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if after.Session.ID != p.Session.ID || after.Account.Version != p.Account.Version || after.Session.AuthorizationEpoch != p.Session.AuthorizationEpoch {
		return out, authority.ErrForbidden
	}
	_, e = tx.ExecContext(ctx, `UPDATE notification_v1.device_challenges SET sent_at=clock_timestamp()WHERE id=$1::uuid AND consumed_at IS NULL AND expires_at>clock_timestamp()`, out.ID)
	if e != nil {
		return out, e
	}
	if _, e = d.authorize(ctx, tx, actor); e != nil {
		return out, e
	}
	e = tx.Commit()
	return out, e
}
func (d *DeviceStore) ConfirmDevice(ctx context.Context, actor string, q notifications.DeviceProof) (out notifications.Device, e error) {
	b, x := base64.RawURLEncoding.DecodeString(q.Code)
	if !authority.ValidID(q.ChallengeID) || x != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != q.Code {
		return out, notifications.ErrInvalid
	}
	tx, p, e := d.begin(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	var digest, hash, sealed []byte
	var hours int
	e = tx.QueryRowContext(ctx, `SELECT device_id::text,label,code_hash,token_hash,sealed_token,consent_hours FROM notification_v1.device_challenges WHERE id=$1::uuid AND account_id=$2::uuid AND session_id=$3::uuid AND account_version=$4::bigint AND authorization_epoch=$5::bigint AND consumed_at IS NULL AND sent_at IS NOT NULL AND expires_at>clock_timestamp()FOR UPDATE`, q.ChallengeID, actor, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch).Scan(&out.ID, &out.Label, &digest, &hash, &sealed, &hours)
	if errors.Is(e, sql.ErrNoRows) {
		return out, authority.ErrForbidden
	}
	if e != nil {
		return
	}
	supplied := sha256.Sum256([]byte(q.Code))
	if subtle.ConstantTimeCompare(digest, supplied[:]) != 1 {
		return out, authority.ErrForbidden
	}
	var owner string
	e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM notification_v1.devices WHERE id=$1::uuid OR token_hash=$2::bytea ORDER BY account_id FOR UPDATE`, out.ID, hash).Scan(&owner)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return
	}
	if e == nil && owner != actor {
		return out, notifications.ErrConflict
	}
	e = nil
	out.Provider = "fcm"
	out.TokenHash = hex.EncodeToString(hash)
	e = tx.QueryRowContext(ctx, `INSERT INTO notification_v1.devices(id,account_id,label,provider,token_hash,sealed_token,session_id,account_version,authorization_epoch,created_at,verified_at,expires_at)SELECT $1::uuid,$2::uuid,$3::text,'fcm',$4::bytea,$5::bytea,$6::uuid,$7::bigint,$8::bigint,t.now,t.now,LEAST(t.now+$9::integer*interval '1 hour',$10::timestamptz)FROM(SELECT clock_timestamp()now)t ON CONFLICT(id)DO UPDATE SET label=EXCLUDED.label,token_hash=EXCLUDED.token_hash,sealed_token=EXCLUDED.sealed_token,session_id=EXCLUDED.session_id,account_version=EXCLUDED.account_version,authorization_epoch=EXCLUDED.authorization_epoch,verified_at=EXCLUDED.verified_at,expires_at=EXCLUDED.expires_at,revoked_at=NULL WHERE notification_v1.devices.account_id=EXCLUDED.account_id RETURNING created_at,verified_at,expires_at`, out.ID, actor, out.Label, hash, sealed, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, hours, p.Session.ExpiresAt).Scan(&out.CreatedAt, &out.VerifiedAt, &out.ExpiresAt)
	if e != nil {
		var pg *pgconn.PgError
		if errors.As(e, &pg) && pg.Code == "23505" {
			e = notifications.ErrConflict
		}
		return
	}
	if _, e = tx.ExecContext(ctx, `UPDATE notification_v1.device_challenges SET consumed_at=clock_timestamp()WHERE id=$1::uuid`, q.ChallengeID); e != nil {
		return
	}
	if e = d.rewards.ApplyCanonicalEvent(ctx, tx, actor, "account.push_enable", out.ID); e != nil {
		return
	}
	if _, e = d.authorize(ctx, tx, actor); e != nil {
		return
	}
	e = tx.Commit()
	return
}
func (d *DeviceStore) Devices(ctx context.Context, actor string) (out []notifications.Device, e error) {
	out = []notifications.Device{}
	tx, _, e := d.begin(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id::text,label,provider,encode(token_hash,'hex'),created_at,verified_at,expires_at,revoked_at FROM notification_v1.devices WHERE account_id=$1::uuid ORDER BY created_at,id LIMIT 201`, actor)
	if e != nil {
		return
	}
	for rows.Next() {
		var v notifications.Device
		if e = rows.Scan(&v.ID, &v.Label, &v.Provider, &v.TokenHash, &v.CreatedAt, &v.VerifiedAt, &v.ExpiresAt, &v.RevokedAt); e != nil {
			break
		}
		out = append(out, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return
	}
	if len(out) > 200 {
		return nil, notifications.ErrUnavailable
	}
	if _, e = d.authorize(ctx, tx, actor); e != nil {
		return
	}
	e = tx.Commit()
	return
}
func (d *DeviceStore) RevokeDevice(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return notifications.ErrInvalid
	}
	tx, _, e := d.begin(ctx, actor)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `UPDATE notification_v1.devices SET revoked_at=COALESCE(revoked_at,clock_timestamp())WHERE id=$1::uuid AND account_id=$2::uuid`, id, actor)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return notifications.ErrNotFound
	}
	if _, e = d.authorize(ctx, tx, actor); e != nil {
		return e
	}
	return tx.Commit()
}
func (d *DeviceStore) ProcessDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, notifications.ErrInvalid
	}
	res, e := d.db.ExecContext(ctx, `DELETE FROM notification_v1.device_challenges WHERE id IN(SELECT id FROM notification_v1.device_challenges WHERE expires_at<clock_timestamp()-interval '24 hours' ORDER BY expires_at,id LIMIT $1::integer)`, limit)
	if e != nil {
		return 0, e
	}
	n, e := res.RowsAffected()
	return int(n), e
}

// Send transmits only a wake-up identifier. All content is fetched through the
// canonical inbox audience policy; no stale/private notification body escapes.
func (d *DeviceStore) Send(ctx context.Context, delivery notifications.Delivery) error {
	if d == nil {
		return notifications.ErrDeliveryUnconfigured
	}
	if !authority.ValidID(delivery.Notification.AccountID) || !authority.ValidID(delivery.Notification.ID) || !authority.ValidID(delivery.Key) {
		return notifications.ErrInvalid
	}
	tx, e := d.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	actor := delivery.Notification.AccountID
	var id string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor).Scan(&id); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT d.id::text,d.sealed_token,d.session_id::text,d.account_version,d.authorization_epoch,d.expires_at FROM notification_v1.devices d WHERE account_id=$1::uuid AND revoked_at IS NULL AND expires_at>clock_timestamp()AND EXISTS(SELECT 1 FROM owned_authority_sessions s WHERE s.id=d.session_id AND s.type=0 AND s.parent_id IS NULL AND s.client_id='')ORDER BY id LIMIT 201 FOR SHARE`, actor)
	if e != nil {
		return e
	}
	type target struct {
		id, session    string
		sealed         []byte
		version, epoch int64
		expires        time.Time
	}
	all := []target{}
	for rows.Next() {
		var v target
		if e = rows.Scan(&v.id, &v.sealed, &v.session, &v.version, &v.epoch, &v.expires); e != nil {
			break
		}
		all = append(all, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	if len(all) > 200 {
		return notifications.ErrUnavailable
	}
	sent := false
	for _, v := range all {
		valid, e := d.guard.StoredSessionEligible(ctx, d.db, tx, actor, v.session, v.version, v.epoch, notifications.DevicePermission)
		if e != nil {
			return e
		}
		if !valid {
			continue
		}
		token, e := d.openToken(actor, v.id, v.sealed)
		if e != nil {
			return e
		}
		bounded, cancel := context.WithDeadline(ctx, v.expires)
		e = d.provider.SendDevice(bounded, token, map[string]string{"kind": "notifications.wakeup", "notification_id": delivery.Notification.ID, "delivery_key": delivery.Key, "app_id": delivery.Notification.AppID})
		cancel()
		if e != nil {
			return e
		}
		valid, e = d.guard.StoredSessionEligible(ctx, d.db, tx, actor, v.session, v.version, v.epoch, notifications.DevicePermission)
		if e != nil {
			return e
		}
		if !valid {
			return authority.ErrForbidden
		}
		sent = true
	}
	if !sent {
		return notifications.ErrDeliveryUnconfigured
	}
	return tx.Commit()
}
