// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
)

func validate(c *notifications.Command) error {
	if !authority.ValidID(c.AccountID) || !notifications.ValidApp(c.AppID) || len(c.Key) < 1 || len(c.Key) > 256 || strings.TrimSpace(c.Topic) == "" || len(c.Topic) > 128 || strings.TrimSpace(c.Title) == "" || len(c.Title) > 1024 || len(c.Subtitle) > 1024 || len(c.Content) > 8192 || c.Priority < -100 || c.Priority > 100 {
		return notifications.ErrInvalid
	}
	for _, s := range []string{c.Key, c.Topic, c.Title, c.Subtitle, c.Content} {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return notifications.ErrInvalid
		}
	}
	if len(c.Meta) == 0 {
		c.Meta = json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if e := json.Unmarshal(c.Meta, &obj); e != nil || obj == nil || len(c.Meta) > 16384 {
		return notifications.ErrInvalid
	}
	return nil
}

// Enqueue durably records a command with a caller-supplied idempotency key.
// It is a trusted application API, with no public HTTP equivalent.
func (r *Repository) Enqueue(ctx context.Context, c notifications.Command) (string, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	id, e := r.EnqueueInTransaction(ctx, r.db, tx, c)
	if e != nil {
		return "", e
	}
	return id, tx.Commit()
}

// EnqueueInTransaction is trusted in-process infrastructure. The canonical pool
// must match; caller owns rollback/commit. No public route accepts these commands.
func (r *Repository) EnqueueInTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, c notifications.Command) (string, error) {
	if r == nil || db == nil || r.db != db || tx == nil {
		return "", notifications.ErrUnavailable
	}
	if e := validate(&c); e != nil {
		return "", e
	}
	if r == nil || c.AppID != r.app {
		return "", notifications.ErrInvalid
	}
	b, e := json.Marshal(c)
	if e != nil || len(b) > notifications.MaxPayload {
		return "", notifications.ErrInvalid
	}
	hash := sha256.Sum256(b)
	active, e := lockAccount(ctx, tx, c.AccountID)
	if e != nil {
		return "", e
	}
	if !active {
		return "", authority.ErrDisabled
	}
	var id string
	var old []byte
	e = tx.QueryRowContext(ctx, `SELECT id::text,fingerprint FROM notification_v1.outbox WHERE account_id=$1 AND app_id=$2 AND source_key=$3`, c.AccountID, c.AppID, c.Key).Scan(&id, &old)
	if e == nil {
		if !bytes.Equal(old, hash[:]) {
			return "", notifications.ErrConflict
		}
		return id, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	id, e = uuid()
	if e != nil {
		return "", e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.outbox(id,account_id,app_id,source_key,fingerprint,command,created_at) VALUES($1,$2,$3,$4,$5,$6::jsonb,clock_timestamp())`, id, c.AccountID, c.AppID, c.Key, hash[:], string(b))
	if e != nil {
		return "", e
	}
	return id, nil
}

// ProcessPending atomically turns durable commands into inbox items and replay
// events. A crash rolls back both projection and receipt; retries are idempotent.
func (r *Repository) ProcessPending(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, notifications.ErrInvalid
	}
	count := 0
	for ; count < limit; count++ {
		ok, e := r.processOne(ctx)
		if e != nil {
			return count, e
		}
		if !ok {
			return count, nil
		}
	}
	return count, nil
}
func (r *Repository) processOne(ctx context.Context) (bool, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var id, account, app string
	var raw []byte
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,app_id,command FROM notification_v1.outbox WHERE processed_at IS NULL AND app_id=$1 ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, r.app).Scan(&id, &account, &app, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var c notifications.Command
	if e = json.Unmarshal(raw, &c); e != nil {
		return false, e
	}
	if e = validate(&c); e != nil {
		return false, e
	}
	if c.AccountID != account || c.AppID != app {
		return false, notifications.ErrInvalid
	}
	reservedPublisher := isPublisherContentKey(c.Key)
	active := false
	if reservedPublisher {
		active, e = r.publisherContentAllowed(ctx, tx, c.Key, account, app, id)
	} else {
		active, e = lockAccount(ctx, tx, account)
	}
	if e != nil {
		return false, e
	}
	if active {
		if reservedPublisher {
			active, e = r.publisherContentAllowed(ctx, tx, c.Key, account, app, id)
			if e != nil {
				return false, e
			}
		}
	}
	if active {
		level, e := r.preference(ctx, tx, account, app, c.Topic)
		if e != nil {
			return false, e
		}
		if level == "reject" {
			active = false
		}
		if level == "silent" {
			c.External = false
		}
	}
	if active {
		now := time.Now().UTC()
		n := notifications.Notification{ID: id, AccountID: account, AppID: app, Topic: c.Topic, Title: c.Title, Subtitle: c.Subtitle, Content: c.Content, Meta: c.Meta, Priority: c.Priority, CreatedAt: now, UpdatedAt: now}
		_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.inbox(id,account_id,app_id,source_key,topic,title,subtitle,content,meta,priority,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$11) ON CONFLICT(account_id,app_id,source_key) DO NOTHING`, id, account, app, c.Key, c.Topic, c.Title, c.Subtitle, c.Content, string(c.Meta), c.Priority, now)
		if e != nil {
			return false, e
		}
		data, e := json.Marshal(n)
		if e != nil {
			return false, e
		}
		if e = appendEvent(ctx, tx, account, app, "notification:"+id, "notifications.new", "notification", &id, data, now); e != nil {
			return false, e
		}
		if c.External {
			if _, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.deliveries(notification_id,next_attempt_at) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, now); e != nil {
				return false, e
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE notification_v1.outbox SET processed_at=clock_timestamp() WHERE id=$1`, id); e != nil {
		return false, e
	}
	return true, tx.Commit()
}

// DispatchOne leases one external delivery without holding a transaction over
// I/O. Nil Sender explicitly fails, records a retry and never claims delivery.
// Recipient payloads and provider errors are never saved in diagnostic fields.
func (r *Repository) DispatchOne(ctx context.Context, s notifications.Sender) (bool, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var id, sourceKey string
	var attempts int
	e = tx.QueryRowContext(ctx, `SELECT d.notification_id::text,d.attempts,n.source_key FROM notification_v1.deliveries d JOIN notification_v1.inbox n ON n.id=d.notification_id WHERE n.app_id=$1 AND d.delivered_at IS NULL AND d.suppressed_at IS NULL AND d.next_attempt_at<=clock_timestamp() AND (d.lease_until IS NULL OR d.lease_until<clock_timestamp()) ORDER BY d.next_attempt_at,d.notification_id LIMIT 1 FOR UPDATE OF d SKIP LOCKED`, r.app).Scan(&id, &attempts, &sourceKey)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	n, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM notification_v1.inbox WHERE id=$1`, id))
	if e != nil {
		return false, e
	}
	reservedPublisher := isPublisherContentKey(sourceKey)
	active := false
	if reservedPublisher {
		active, e = r.publisherContentAllowed(ctx, tx, sourceKey, n.AccountID, n.AppID, id)
	} else {
		active, e = lockAccount(ctx, tx, n.AccountID)
	}
	if e != nil {
		return false, e
	}
	if active {
		level, e := r.preference(ctx, tx, n.AccountID, n.AppID, n.Topic)
		if e != nil {
			return false, e
		}
		active = level == "normal"
	}
	if !active || n.DismissedAt != nil {
		_, e = tx.ExecContext(ctx, `UPDATE notification_v1.deliveries SET suppressed_at=clock_timestamp(),lease_id=NULL,lease_until=NULL WHERE notification_id=$1`, id)
		if e != nil {
			return false, e
		}
		return true, tx.Commit()
	}
	if reservedPublisher {
		active, e = r.publisherContentAllowed(ctx, tx, sourceKey, n.AccountID, n.AppID, id)
		if e != nil {
			return false, e
		}
		if !active {
			_, e = tx.ExecContext(ctx, `UPDATE notification_v1.deliveries SET suppressed_at=clock_timestamp(),lease_id=NULL,lease_until=NULL WHERE notification_id=$1`, id)
			if e != nil {
				return false, e
			}
			return true, tx.Commit()
		}
	}
	lease, e := uuid()
	if e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE notification_v1.deliveries SET lease_id=$2,lease_until=clock_timestamp()+interval '30 seconds',attempts=attempts+1 WHERE notification_id=$1`, id, lease)
	if e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	if s == nil {
		s = notifications.UnconfiguredSender{}
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	sendErr := s.Send(sendCtx, notifications.Delivery{Key: id, Notification: n})
	cancel()
	// A canceled worker leaves the finite lease for a replacement to reclaim.
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if sendErr == nil {
		_, e = r.db.ExecContext(ctx, `UPDATE notification_v1.deliveries SET delivered_at=clock_timestamp(),lease_id=NULL,lease_until=NULL,last_error=NULL WHERE notification_id=$1 AND lease_id=$2`, id, lease)
		return true, e
	}
	// FCM cannot safely replay an uncertain acceptance. End this outer queue
	// item without claiming delivery; otherwise its durable device receipt
	// would be leased and rejected forever. In-progress fences remain retryable.
	if errors.Is(sendErr, notifications.ErrDeliveryUncertain) {
		_, e = r.db.ExecContext(ctx, `UPDATE notification_v1.deliveries SET suppressed_at=clock_timestamp(),lease_id=NULL,lease_until=NULL,last_error='DELIVERY_UNCERTAIN'WHERE notification_id=$1 AND lease_id=$2`, id, lease)
		if e != nil {
			return true, e
		}
		return true, sendErr
	}
	code := "DELIVERY_FAILED"
	if errors.Is(sendErr, notifications.ErrDeliveryUnconfigured) {
		code = "DELIVERY_UNCONFIGURED"
	} else if errors.Is(sendErr, notifications.ErrDeliveryInProgress) {
		code = "DELIVERY_IN_PROGRESS"
	}
	_, e = r.db.ExecContext(ctx, `UPDATE notification_v1.deliveries SET lease_id=NULL,lease_until=NULL,last_error=$3,next_attempt_at=clock_timestamp()+$4::double precision*interval '1 second' WHERE notification_id=$1 AND lease_id=$2`, id, lease, code, RetryDelay(attempts+1).Seconds())
	if e != nil {
		return true, e
	}
	return true, sendErr
}
func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		return 5 * time.Minute
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}
