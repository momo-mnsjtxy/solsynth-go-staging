// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/notifications"
)

// ProjectWallet consumes only the existing owned wallet outbox. Recipients come
// from canonical SQL owners, never event payload account IDs. Realm recipients
// are deliberately omitted pending an event-time/current grant policy.
func (r *Repository) ProjectWallet(ctx context.Context, limit int) (int, error) {
	return r.project(ctx, "wallet", limit, 0)
}

// ProjectChat turns committed owned room sync rows into invalidation events.
// These owned.chat.changed packets intentionally do not pretend to be complete
// upstream messages.new objects; clients fetch/sync through authorized APIs.
// noneEncryptionMode must be the host's explicit owned room policy.
func (r *Repository) ProjectChat(ctx context.Context, limit, noneEncryptionMode int) (int, error) {
	return r.project(ctx, "chat", limit, noneEncryptionMode)
}
func (r *Repository) project(ctx context.Context, source string, limit, mode int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, notifications.ErrInvalid
	}
	for n := 0; n < limit; n++ {
		ok, e := r.projectOne(ctx, source, mode)
		if e != nil {
			return n, e
		}
		if !ok {
			return n, nil
		}
	}
	return limit, nil
}
func (r *Repository) projectOne(ctx context.Context, source string, mode int) (bool, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	// One projector per source/tenant is enough; a nonblocking advisory lock
	// avoids duplicate fanout and preserves committed source ordering on replicas.
	var acquired bool
	if e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "notification_v1:"+source+":"+r.app).Scan(&acquired); e != nil {
		return false, e
	}
	if !acquired {
		return false, nil
	}
	var id, aggregate, kind string
	var data []byte
	var at time.Time
	if source == "wallet" {
		e = tx.QueryRowContext(ctx, `SELECT o.event_id::text,o.aggregate_id::text,o.event_type,o.payload,o.created_at FROM wallet_v1.outbox o WHERE NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='wallet' AND p.source_id=o.event_id AND p.app_id=$1) ORDER BY o.sequence LIMIT 1`, r.app).Scan(&id, &aggregate, &kind, &data, &at)
	} else {
		var seq int64
		e = tx.QueryRowContext(ctx, `SELECT m.id::text,m.chat_room_id::text,m.type,m.room_sequence,m.created_at FROM chat_messages m JOIN chat_rooms r ON r.id=m.chat_room_id WHERE r.owned_owner_id IS NOT NULL AND r.encryption_mode=$2 AND m.type IN('text','messages.update','messages.delete') AND NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat' AND p.source_id=m.id AND p.app_id=$1) ORDER BY m.created_at,m.chat_room_id,m.room_sequence LIMIT 1`, r.app, mode).Scan(&id, &aggregate, &kind, &seq, &at)
		if e == nil {
			data, e = json.Marshal(map[string]any{"chat_room_id": aggregate, "message_id": id, "room_sequence": seq, "mutation": kind})
			kind = "owned.chat.changed"
		}
	}
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var rows *sql.Rows
	if source == "chat" {
		rows, e = tx.QueryContext(ctx, `SELECT DISTINCT m.account_id::text FROM chat_members m JOIN chat_rooms r ON r.id=m.chat_room_id WHERE m.chat_room_id=$1 AND m.joined_at IS NOT NULL AND m.joined_at<=$2 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND r.deleted_at IS NULL AND `+r.chatFilter("r.id", "m.account_id")+` ORDER BY m.account_id::text`, aggregate, at)
	} else if kind == "wallet.fund.created" || kind == "wallet.fund.updated" {
		rows, e = tx.QueryContext(ctx, `SELECT creator_account_id::text FROM wallet_v1.funds WHERE id=$1 UNION SELECT account_id::text FROM wallet_v1.fund_participants WHERE fund_id=$1 ORDER BY 1`, aggregate)
	} else if strings.HasPrefix(kind, "wallet.transaction.") {
		rows, e = tx.QueryContext(ctx, `SELECT DISTINCT w.account_id::text FROM wallet_v1.transactions t JOIN wallet_v1.wallets w ON w.id IN(t.payer_wallet_id,t.payee_wallet_id) WHERE t.id=$1 AND w.account_id IS NOT NULL AND w.deleted_at IS NULL ORDER BY w.account_id::text`, aggregate)
	} else {
		switch kind {
		case "wallet.created", "wallet.default.changed", "wallet.public_id.changed", "wallet.pocket.updated":
			rows, e = tx.QueryContext(ctx, `SELECT account_id::text FROM wallet_v1.wallets WHERE id=$1 AND account_id IS NOT NULL AND deleted_at IS NULL`, aggregate)
		default:
			return false, notifications.ErrInvalid
		}
	}
	if e != nil {
		return false, e
	}
	accounts := []string{}
	for rows.Next() {
		var a string
		if e = rows.Scan(&a); e != nil {
			rows.Close()
			return false, e
		}
		accounts = append(accounts, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	// Every source's recipients are sorted, so multi-account account locks cannot
	// deadlock with another projector using the same canonical locking order.
	for _, a := range accounts {
		active, e := lockAccount(ctx, tx, a)
		if e != nil {
			return false, e
		}
		if !active {
			continue
		}
		aud := "account"
		var audienceID *string
		if source == "chat" {
			aud = "chat"
			audienceID = &aggregate
		}
		if e = appendEvent(ctx, tx, a, r.app, source+":"+id, kind, aud, audienceID, data, at); e != nil {
			return false, e
		}
		if source == "wallet" && (kind == "wallet.transaction.created" || kind == "wallet.transaction.confirmed" || kind == "wallet.transaction.refunded" || kind == "wallet.transaction.expired") {
			if e = r.walletNotification(ctx, tx, a, r.app, id, aggregate, kind, at); e != nil {
				return false, e
			}
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.receipts(source,source_id,app_id,processed_at) VALUES($1,$2,$3,clock_timestamp())`, source, id, r.app)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}

// Owned summaries make existing wallet writes populate the durable inbox.
// Copy is deliberately identified as owned; no upstream wording or private
// remarks are inferred. Both participant recipients come from canonical SQL.
func (r *Repository) walletNotification(ctx context.Context, tx *sql.Tx, account, app, event, transaction, kind string, at time.Time) error {
	level, e := r.preference(ctx, tx, account, app, "wallets.transactions")
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
	meta, e := json.Marshal(map[string]string{"transaction_id": transaction, "event_type": kind})
	if e != nil {
		return e
	}
	n := notifications.Notification{ID: id, AccountID: account, AppID: app, Topic: "wallets.transactions", Title: "Wallet transaction updated", Content: "A transaction involving your wallet was updated.", Meta: meta, CreatedAt: at, UpdatedAt: at}
	_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.inbox(id,account_id,app_id,source_key,topic,title,subtitle,content,meta,priority,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'',$7,$8::jsonb,0,$9,$9)`, id, account, app, "wallet:"+event, n.Topic, n.Title, n.Content, string(meta), at)
	if e != nil {
		return e
	}
	data, e := json.Marshal(n)
	if e != nil {
		return e
	}
	return appendEvent(ctx, tx, account, app, "wallet-notification:"+event, "notifications.new", "notification", &id, data, at)
}

type Worker struct {
	Repository             *Repository
	Wallet                 bool
	Chat                   bool
	ChatNoneEncryptionMode int
	Sender                 notifications.Sender
}

// Step is finite, cancellation-aware and safe to run from cooperating replicas.
// External attempts are bounded independently of local inbox/event projection.
func (w Worker) Step(ctx context.Context) (int, error) {
	if w.Repository == nil {
		return 0, notifications.ErrUnavailable
	}
	n, e := w.Repository.ProcessPending(ctx, 32)
	if e != nil {
		return n, e
	}
	if w.Wallet {
		v, e := w.Repository.ProjectWallet(ctx, 32)
		n += v
		if e != nil {
			return n, e
		}
	}
	if w.Chat {
		v, e := w.Repository.ProjectChat(ctx, 32, w.ChatNoneEncryptionMode)
		n += v
		if e != nil {
			return n, e
		}
	}
	sent, e := w.Repository.DispatchOne(ctx, w.Sender)
	if sent {
		n++
	}
	return n, e
}

// Run continues through retryable failures; onError receives only a stable
// diagnostic code so provider errors cannot expose credentials or recipients.
func (w Worker) Run(ctx context.Context, onError func(string)) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, e := w.Step(ctx)
		if e != nil && onError != nil {
			code := "NOTIFICATION_WORKER_FAILED"
			if errors.Is(e, notifications.ErrDeliveryUnconfigured) {
				code = "NOTIFICATION_DELIVERY_UNCONFIGURED"
			}
			onError(code)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
