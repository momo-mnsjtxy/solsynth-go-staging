// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
)

type ChatEventInspection struct {
	PendingMessages  int64      `json:"pending_messages"`
	PendingReads     int64      `json:"pending_read_receipts"`
	StreamReferences int64      `json:"stream_references"`
	OldestPending    *time.Time `json:"oldest_pending_at,omitempty"`
}

// InspectChatEvents returns counts and timestamps only; message bodies and
// recipient identifiers never cross the maintenance boundary.
func (r *Repository) InspectChatEvents(ctx context.Context, noneMode int) (ChatEventInspection, error) {
	var v ChatEventInspection
	if r == nil || r.db == nil {
		return v, notifications.ErrUnavailable
	}
	var oldest sql.NullTime
	e := r.db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM chat_messages m JOIN chat_rooms c ON c.id=m.chat_room_id WHERE c.owned_owner_id IS NOT NULL AND c.encryption_mode IN($3,3) AND m.type IN('text','voice','placeholder','messages.update','messages.delete','messages.sync.finalize','messages.placeholder.update','messages.placeholder.expired') AND NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat-live-v1' AND p.source_id=m.id AND p.app_id=$1)),
 (SELECT count(*) FROM owned_chat_read_events x WHERE NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat-read-v1' AND p.source_id=x.id AND p.app_id=$1)),
 (SELECT count(*) FROM notification_v1.events WHERE app_id=$1 AND type=$2),
 (SELECT min(at) FROM(SELECT min(m.created_at) at FROM chat_messages m JOIN chat_rooms c ON c.id=m.chat_room_id WHERE c.owned_owner_id IS NOT NULL AND c.encryption_mode IN($3,3) AND m.type IN('text','voice','placeholder','messages.update','messages.delete','messages.sync.finalize','messages.placeholder.update','messages.placeholder.expired') AND NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat-live-v1' AND p.source_id=m.id AND p.app_id=$1) UNION ALL SELECT min(x.created_at) FROM owned_chat_read_events x WHERE NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat-read-v1' AND p.source_id=x.id AND p.app_id=$1)) pending)`, r.app, chatLiveMarker, noneMode).Scan(&v.PendingMessages, &v.PendingReads, &v.StreamReferences, &oldest)
	if oldest.Valid {
		v.OldestPending = &oldest.Time
	}
	return v, e
}

// RedriveChatEvent removes the projection receipt and prior recipient
// references atomically. The next worker pass rebuilds recipients and payloads
// from current canonical state at new stream sequences.
func (r *Repository) RedriveChatEvent(ctx context.Context, kind, id string) error {
	if r == nil || r.db == nil || !authority.ValidID(id) || kind != "message" && kind != "read" {
		return notifications.ErrInvalid
	}
	source, key := "chat-live-v1", "chat-live-v1:"+id
	if kind == "read" {
		source, key = "chat-read-v1", "chat-read-v1:"+id
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "notification_v1:chat-maintenance:"+r.app); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT DISTINCT account_id::text FROM notification_v1.events WHERE app_id=$1 AND source_key=$2 ORDER BY account_id::text`, r.app, key)
	if e != nil {
		return e
	}
	accounts := []string{}
	for rows.Next() {
		var account string
		if e = rows.Scan(&account); e != nil {
			rows.Close()
			return e
		}
		accounts = append(accounts, account)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, account := range accounts {
		if _, e = lockAccount(ctx, tx, account); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM notification_v1.events WHERE app_id=$1 AND source_key=$2`, r.app, key); e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM notification_v1.receipts WHERE source=$1 AND source_id=$2 AND app_id=$3`, source, id, r.app)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return notifications.ErrNotFound
	}
	return tx.Commit()
}

// PurgeStreamEvents advances each affected account floor only across a deleted
// contiguous prefix. This makes old cursors fail explicitly instead of silently
// skipping retained events. It intentionally applies to the shared stream.
func (r *Repository) PurgeStreamEvents(ctx context.Context, before time.Time, limit int) (int, error) {
	if r == nil || r.db == nil || before.IsZero() || limit < 1 || limit > 10000 {
		return 0, notifications.ErrInvalid
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var locked bool
	if e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "notification_v1:retention:"+r.app).Scan(&locked); e != nil || !locked {
		return 0, e
	}
	// Discover without taking a stream lock. Every event writer locks the
	// canonical account before advancing its stream, so retention must use the
	// same account -> stream order across all instances.
	var account string
	e = tx.QueryRowContext(ctx, `SELECT s.account_id::text FROM notification_v1.streams s WHERE s.app_id=$1 AND (SELECT e.created_at FROM notification_v1.events e WHERE e.account_id=s.account_id AND e.app_id=s.app_id AND e.sequence>s.floor ORDER BY e.sequence LIMIT 1)<$2 ORDER BY s.account_id LIMIT 1`, r.app, before).Scan(&account)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	if _, e = lockAccount(ctx, tx, account); e != nil {
		return 0, e
	}
	// The discovery snapshot may be stale after the account-lock wait. Lock the
	// stream only now, then derive the deletable prefix again from current rows.
	var floor int64
	e = tx.QueryRowContext(ctx, `SELECT floor FROM notification_v1.streams WHERE account_id=$1 AND app_id=$2 FOR UPDATE`, account, r.app).Scan(&floor)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	var cutoff sql.NullInt64
	e = tx.QueryRowContext(ctx, `SELECT max(sequence) FROM(SELECT candidate.sequence FROM notification_v1.events candidate WHERE candidate.account_id=$1 AND candidate.app_id=$2 AND candidate.sequence>$3 AND candidate.created_at<$4 AND NOT EXISTS(SELECT 1 FROM notification_v1.events stop WHERE stop.account_id=$1 AND stop.app_id=$2 AND stop.sequence<candidate.sequence AND stop.sequence>$3 AND stop.created_at>=$4) ORDER BY candidate.sequence LIMIT $5) prefix`, account, r.app, floor, before, limit).Scan(&cutoff)
	if e != nil || !cutoff.Valid {
		return 0, e
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM notification_v1.events WHERE account_id=$1 AND app_id=$2 AND sequence>$3 AND sequence<=$4`, account, r.app, floor, cutoff.Int64)
	if e != nil {
		return 0, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE notification_v1.streams SET floor=$3 WHERE account_id=$1 AND app_id=$2`, account, r.app, cutoff.Int64); e != nil {
		return 0, e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_chat_read_events source WHERE source.created_at<$1 AND EXISTS(SELECT 1 FROM notification_v1.receipts receipt WHERE receipt.source='chat-read-v1' AND receipt.source_id=source.id AND receipt.app_id=$2) AND NOT EXISTS(SELECT 1 FROM notification_v1.events event WHERE event.app_id=$2 AND event.type=$3 AND event.data->>'receipt_id'=source.id::text)`, before, r.app, chatLiveMarker); e != nil {
		return 0, e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return 0, e
	}
	return int(n), tx.Commit()
}
