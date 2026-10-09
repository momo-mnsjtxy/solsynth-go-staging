// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/notifications"
	"time"
)

// ProjectLocalInTransaction projects trusted, already-authorized domain work
// into the local inbox and replay stream in the same read-committed transaction.
// The caller MUST lock the recipient account before domain locks and perform
// current audience checks. It cannot request external delivery. Passing the
// exact configured pool is required, matching the transaction-guard contract.
func (r *Repository) ProjectLocalInTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, c notifications.Command) error {
	if r == nil || r.db == nil || db != r.db || tx == nil || c.AppID != r.app || c.External {
		return notifications.ErrUnavailable
	}
	if e := validate(&c); e != nil {
		return e
	}
	var isolation string
	if e := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); e != nil {
		return e
	}
	if isolation != "read committed" {
		return notifications.ErrUnavailable
	}
	active, e := lockAccount(ctx, tx, c.AccountID)
	if e != nil {
		return e
	}
	if !active {
		return nil
	}
	level, e := r.preference(ctx, tx, c.AccountID, c.AppID, c.Topic)
	if e != nil {
		return e
	}
	if level == "reject" {
		return nil
	}
	id, e := uuid()
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	// Source keys are owned domain event IDs. On a crash, both this insert and
	// its domain receipt roll back. A replay cannot manufacture a second item.
	result, e := tx.ExecContext(ctx, `INSERT INTO notification_v1.inbox(id,account_id,app_id,source_key,topic,title,subtitle,content,meta,priority,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$11) ON CONFLICT(account_id,app_id,source_key) DO NOTHING`, id, c.AccountID, c.AppID, c.Key, c.Topic, c.Title, c.Subtitle, c.Content, string(c.Meta), c.Priority, now)
	if e != nil {
		return e
	}
	count, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if count == 0 {
		return nil
	}
	n := notifications.Notification{ID: id, AccountID: c.AccountID, AppID: c.AppID, Topic: c.Topic, Title: c.Title, Subtitle: c.Subtitle, Content: c.Content, Meta: c.Meta, Priority: c.Priority, CreatedAt: now, UpdatedAt: now}
	data, e := json.Marshal(n)
	if e != nil {
		return e
	}
	return appendEvent(ctx, tx, c.AccountID, c.AppID, "notification:"+id, "notifications.new", "notification", &id, data, now)
}
