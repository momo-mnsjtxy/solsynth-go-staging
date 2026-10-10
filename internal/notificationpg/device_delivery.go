// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"time"
)

// WithDeliveryPolicy uses the very same current audience and preference policy
// as the canonical inbox. Configure before any request or worker goroutine.
func (d *DeviceStore) WithDeliveryPolicy(p *Repository) *DeviceStore {
	d.policy = p
	return d
}

func lockDeliveryAccount(ctx context.Context, tx *sql.Tx, actor string) (bool, error) {
	if _, e := lockAccount(ctx, tx, actor); e != nil {
		return false, e
	}
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE id=$1::uuid AND NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account'AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())))`, actor).Scan(&active)
	return active, e
}

func (d *DeviceStore) deliveryAllowed(ctx context.Context, tx *sql.Tx, v notifications.Delivery) (bool, error) {
	if d.policy == nil || d.policy.db != d.db || d.policy.guard != d.guard || d.policy.app != v.Notification.AppID {
		return false, notifications.ErrDeliveryUnconfigured
	}
	var topic string
	e := tx.QueryRowContext(ctx, `SELECT topic FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2::text AND id=$3::uuid AND dismissed_at IS NULL AND `+d.policy.inboxVisibilitySQL()+` AND EXISTS(SELECT 1 FROM notification_v1.deliveries e WHERE e.notification_id=inbox.id AND e.suppressed_at IS NULL)`, v.Notification.AccountID, v.Notification.AppID, v.Key).Scan(&topic)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	level, e := d.policy.preference(ctx, tx, v.Notification.AccountID, v.Notification.AppID, topic)
	return level == "normal", e
}

// A manifest freezes the current device audience once. A retry never admits a
// newly registered device or substitutes a renewed device's new consent proof.
func (d *DeviceStore) deliveryManifest(ctx context.Context, v notifications.Delivery) ([]string, error) {
	tx, e := d.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	active, e := lockDeliveryAccount(ctx, tx, v.Notification.AccountID)
	if e != nil {
		return nil, e
	}
	if !active {
		return nil, notifications.ErrDeliveryUnconfigured
	}
	allowed, e := d.deliveryAllowed(ctx, tx, v)
	if e != nil {
		return nil, e
	}
	if !allowed {
		return nil, notifications.ErrDeliveryUnconfigured
	}
	res, e := tx.ExecContext(ctx, `INSERT INTO notification_v1.device_delivery_batches(delivery_id,account_id,app_id,created_at)VALUES($1::uuid,$2::uuid,$3::text,clock_timestamp())ON CONFLICT DO NOTHING`, v.Key, v.Notification.AccountID, v.Notification.AppID)
	if e != nil {
		return nil, e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return nil, e
	}
	if n != 0 {
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_v1.devices WHERE account_id=$1::uuid AND revoked_at IS NULL AND expires_at>clock_timestamp()`, v.Notification.AccountID).Scan(&count); e != nil {
			return nil, e
		}
		if count > 200 {
			return nil, notifications.ErrUnavailable
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.device_delivery_receipts(delivery_id,device_id,token_hash,session_id,account_version,authorization_epoch,verified_at,expires_at,state)SELECT $1::uuid,id,token_hash,session_id,account_version,authorization_epoch,verified_at,expires_at,'pending' FROM notification_v1.devices WHERE account_id=$2::uuid AND revoked_at IS NULL AND expires_at>clock_timestamp() ORDER BY id`, v.Key, v.Notification.AccountID)
		if e != nil {
			return nil, e
		}
	}
	rows, e := tx.QueryContext(ctx, `SELECT r.device_id::text FROM notification_v1.device_delivery_receipts r JOIN notification_v1.device_delivery_batches b ON b.delivery_id=r.delivery_id WHERE r.delivery_id=$1::uuid AND b.account_id=$2::uuid AND b.app_id=$3::text ORDER BY r.device_id`, v.Key, v.Notification.AccountID, v.Notification.AppID)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return ids, nil
}

type deviceDispatch struct {
	id, session, attempt string
	hash                 []byte
	version, epoch       int64
	verified, expires    time.Time
}

func (d *DeviceStore) dispatchEligible(ctx context.Context, tx *sql.Tx, actor string, v deviceDispatch) (string, bool, error) {
	var sealed, hash []byte
	var session string
	var version, epoch int64
	var verified, expires time.Time
	var root bool
	e := tx.QueryRowContext(ctx, `SELECT d.sealed_token,d.token_hash,d.session_id::text,d.account_version,d.authorization_epoch,d.verified_at,d.expires_at,s.type=0 AND s.parent_id IS NULL AND s.client_id='' FROM notification_v1.devices d JOIN owned_authority_sessions s ON s.id=d.session_id AND s.account_id=d.account_id WHERE d.id=$1::uuid AND d.account_id=$2::uuid AND d.revoked_at IS NULL AND d.expires_at>clock_timestamp() FOR SHARE OF d`, v.id, actor).Scan(&sealed, &hash, &session, &version, &epoch, &verified, &expires, &root)
	if errors.Is(e, sql.ErrNoRows) {
		return "", false, nil
	}
	if e != nil {
		return "", false, e
	}
	if !root || !bytes.Equal(hash, v.hash) || session != v.session || version != v.version || epoch != v.epoch || !verified.Equal(v.verified) || !expires.Equal(v.expires) {
		return "", false, nil
	}
	valid, e := d.guard.StoredSessionEligible(ctx, d.db, tx, actor, v.session, v.version, v.epoch, notifications.DevicePermission)
	if e != nil || !valid {
		return "", valid, e
	}
	token, e := d.openToken(actor, v.id, sealed)
	return token, e == nil, e
}

// claimDevice commits the attempt fence before external I/O. A stale sending
// fence is uncertain, never reclaimed for another provider call: FCM has no
// server idempotency API for the acceptance-before-ack crash window.
func (d *DeviceStore) claimDevice(ctx context.Context, delivery notifications.Delivery, id string) (v deviceDispatch, token, state string, e error) {
	tx, e := d.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer tx.Rollback()
	active, e := lockDeliveryAccount(ctx, tx, delivery.Notification.AccountID)
	if e != nil {
		return
	}
	allowed := false
	if active {
		allowed, e = d.deliveryAllowed(ctx, tx, delivery)
		if e != nil {
			return
		}
	}
	v.id = id
	var lease sql.NullTime
	e = tx.QueryRowContext(ctx, `SELECT token_hash,session_id::text,account_version,authorization_epoch,verified_at,expires_at,state,lease_until FROM notification_v1.device_delivery_receipts WHERE delivery_id=$1::uuid AND device_id=$2::uuid FOR UPDATE`, delivery.Key, id).Scan(&v.hash, &v.session, &v.version, &v.epoch, &v.verified, &v.expires, &state, &lease)
	if e != nil {
		return
	}
	if state == "sending" {
		var expired bool
		e = tx.QueryRowContext(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, lease.Time).Scan(&expired)
		if e != nil {
			return
		}
		if expired {
			_, e = tx.ExecContext(ctx, `UPDATE notification_v1.device_delivery_receipts SET state='uncertain',attempt_id=NULL,lease_until=NULL,completed_at=clock_timestamp()WHERE delivery_id=$1::uuid AND device_id=$2::uuid`, delivery.Key, id)
			state = "uncertain"
		}
		if e == nil {
			e = tx.Commit()
		}
		return
	}
	if state != "pending" {
		e = tx.Commit()
		return
	}
	valid := false
	if allowed {
		token, valid, e = d.dispatchEligible(ctx, tx, delivery.Notification.AccountID, v)
		if e != nil {
			return
		}
	}
	// Resample audience and recorded proof after the device SHARE wait.
	if valid {
		allowed, e = d.deliveryAllowed(ctx, tx, delivery)
		if e != nil {
			return
		}
		valid = allowed
	}
	if !valid {
		state = "suppressed"
		_, e = tx.ExecContext(ctx, `UPDATE notification_v1.device_delivery_receipts SET state='suppressed',completed_at=clock_timestamp()WHERE delivery_id=$1::uuid AND device_id=$2::uuid`, delivery.Key, id)
	} else {
		v.attempt, e = uuid()
		if e != nil {
			return
		}
		state = "sending"
		_, e = tx.ExecContext(ctx, `UPDATE notification_v1.device_delivery_receipts SET state='sending',attempt_id=$3::uuid,lease_until=clock_timestamp()+interval '30 seconds'WHERE delivery_id=$1::uuid AND device_id=$2::uuid`, delivery.Key, id, v.attempt)
	}
	if e == nil && v.attempt != "" {
		_, valid, x := d.dispatchEligible(ctx, tx, delivery.Notification.AccountID, v)
		if x != nil {
			e = x
		} else {
			allowed, e = d.deliveryAllowed(ctx, tx, delivery)
			if e == nil && (!valid || !allowed) {
				state = "suppressed"
				v.attempt = ""
				_, e = tx.ExecContext(ctx, `UPDATE notification_v1.device_delivery_receipts SET state='suppressed',attempt_id=NULL,lease_until=NULL,completed_at=clock_timestamp()WHERE delivery_id=$1::uuid AND device_id=$2::uuid`, delivery.Key, id)
			}
		}
	}
	if e == nil {
		e = tx.Commit()
	}
	return
}

func (d *DeviceStore) finishDevice(ctx context.Context, delivery notifications.Delivery, v deviceDispatch, sendErr error) error {
	tx, e := d.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	active, e := lockDeliveryAccount(ctx, tx, delivery.Notification.AccountID)
	if e != nil {
		return e
	}
	allowed := false
	if active {
		allowed, e = d.deliveryAllowed(ctx, tx, delivery)
		if e != nil {
			return e
		}
	}
	valid := false
	if allowed {
		_, valid, e = d.dispatchEligible(ctx, tx, delivery.Notification.AccountID, v)
		if e != nil {
			return e
		}
	}
	state := "accepted"
	if sendErr != nil {
		state = "uncertain"
	}
	res, e := tx.ExecContext(ctx, `UPDATE notification_v1.device_delivery_receipts SET state=$4::text,attempt_id=NULL,lease_until=NULL,accepted_at=CASE WHEN $4::text='accepted'THEN clock_timestamp()END,completed_at=clock_timestamp()WHERE delivery_id=$1::uuid AND device_id=$2::uuid AND state='sending'AND attempt_id=$3::uuid AND lease_until>clock_timestamp()`, delivery.Key, v.id, v.attempt, state)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return notifications.ErrDeliveryUncertain
	}
	if valid {
		_, valid, e = d.dispatchEligible(ctx, tx, delivery.Notification.AccountID, v)
		if e != nil {
			return e
		}
		if valid {
			valid, e = d.deliveryAllowed(ctx, tx, delivery)
			if e != nil {
				return e
			}
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if sendErr != nil {
		return notifications.ErrDeliveryUncertain
	}
	if !valid {
		return authority.ErrForbidden
	}
	return nil
}

// Send transmits only public wake-up IDs. Accepted device receipts survive
// partial failure and restart. Uncertain acceptance never triggers blind
// resends or a false delivery acknowledgment; the durable inbox remains usable.
func (d *DeviceStore) Send(ctx context.Context, delivery notifications.Delivery) error {
	if d == nil || d.policy == nil {
		return notifications.ErrDeliveryUnconfigured
	}
	if !authority.ValidID(delivery.Notification.AccountID) || !authority.ValidID(delivery.Notification.ID) || delivery.Key != delivery.Notification.ID || !notifications.ValidApp(delivery.Notification.AppID) {
		return notifications.ErrInvalid
	}
	ids, e := d.deliveryManifest(ctx, delivery)
	if e != nil {
		return e
	}
	accepted := false
	var pendingErr error
	for _, id := range ids {
		v, token, state, e := d.claimDevice(ctx, delivery, id)
		if e != nil {
			return e
		}
		switch state {
		case "accepted":
			accepted = true
			continue
		case "suppressed":
			continue
		case "uncertain":
			pendingErr = notifications.ErrDeliveryUncertain
			continue
		case "sending":
			if v.attempt == "" {
				pendingErr = notifications.ErrDeliveryInProgress
				continue
			}
		}
		deadline := time.Now().Add(10 * time.Second)
		if v.expires.Before(deadline) {
			deadline = v.expires
		}
		bounded, cancel := context.WithDeadline(ctx, deadline)
		sendErr := d.provider.SendDevice(bounded, token, map[string]string{"kind": "notifications.wakeup", "notification_id": delivery.Notification.ID, "delivery_key": delivery.Key, "app_id": delivery.Notification.AppID})
		cancel()
		if e = d.finishDevice(ctx, delivery, v, sendErr); e != nil {
			pendingErr = errors.Join(pendingErr, e)
		} else {
			accepted = true
		}
	}
	if pendingErr != nil {
		return pendingErr
	}
	if !accepted {
		return notifications.ErrDeliveryUnconfigured
	}
	return nil
}
