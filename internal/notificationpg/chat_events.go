// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/notifications"
)

const chatLiveMarker = "owned.chat.live-reference.v1"

func (r *Repository) WithChatEvents(p notifications.ChatEventProjector) *Repository {
	r.chatEvents = p
	return r
}

// ProjectChatEvents appends only recipient-bound references. Message bodies,
// sender profiles, media and encryption metadata are resolved for the current
// authenticated connection at replay and again immediately before each write.
func (r *Repository) ProjectChatEvents(ctx context.Context, limit, noneMode int) (int, error) {
	if r == nil || r.chatEvents == nil {
		return 0, nil
	}
	if limit < 1 || limit > 100 {
		return 0, notifications.ErrInvalid
	}
	for n := 0; n < limit; n++ {
		ok, e := r.projectChatEvent(ctx, noneMode)
		if e != nil {
			return n, e
		}
		if !ok {
			return n, nil
		}
	}
	return limit, nil
}

func (r *Repository) projectChatEvent(ctx context.Context, noneMode int) (bool, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var locked bool
	if e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "notification_v1:chat-live:"+r.app).Scan(&locked); e != nil || !locked {
		return false, e
	}
	var kind, mid, rid string
	var at time.Time
	e = tx.QueryRowContext(ctx, `SELECT kind,id,room_id,created_at FROM(
 SELECT 'message' kind,m.id::text id,m.chat_room_id::text room_id,m.created_at FROM chat_messages m JOIN chat_rooms c ON c.id=m.chat_room_id WHERE c.owned_owner_id IS NOT NULL AND c.encryption_mode IN($2,3) AND m.type IN('text','voice','placeholder','messages.update','messages.delete','messages.sync.finalize','messages.placeholder.update','messages.placeholder.expired') AND NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat-live-v1' AND p.source_id=m.id AND p.app_id=$1)
 UNION ALL
 SELECT 'read',e.id::text,e.room_id::text,e.created_at FROM owned_chat_read_events e WHERE NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat-read-v1' AND p.source_id=e.id AND p.app_id=$1)
) pending ORDER BY created_at,kind,id LIMIT 1`, r.app, noneMode).Scan(&kind, &mid, &rid, &at)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if kind == "read" {
		return r.projectChatReadEvent(ctx, tx)
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.account_id::text,m.id::text,m.joined_at FROM chat_members m JOIN chat_rooms r ON r.id=m.chat_room_id WHERE m.chat_room_id=$1 AND m.joined_at IS NOT NULL AND m.joined_at<=$2 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND r.deleted_at IS NULL AND `+r.chatFilter("r.id", "m.account_id")+` ORDER BY m.account_id::text`, rid, at)
	if e != nil {
		return false, e
	}
	type recipient struct {
		account, member string
		joined          time.Time
	}
	recipients := []recipient{}
	for rows.Next() {
		var v recipient
		if e = rows.Scan(&v.account, &v.member, &v.joined); e != nil {
			rows.Close()
			return false, e
		}
		recipients = append(recipients, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, v := range recipients {
		active, e := lockAccount(ctx, tx, v.account)
		if e != nil {
			return false, e
		}
		if !active {
			continue
		}
		// Recheck after the account-lock wait; an old recipient list is not a grant.
		var eligible bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_members m JOIN chat_rooms r ON r.id=m.chat_room_id WHERE m.account_id=$1 AND m.id=$2 AND m.joined_at=$3 AND m.chat_room_id=$4 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND r.deleted_at IS NULL AND `+r.chatFilter("r.id", "$1")+`)`, v.account, v.member, v.joined, rid).Scan(&eligible)
		if e != nil {
			return false, e
		}
		if !eligible {
			continue
		}
		data, e := json.Marshal(notifications.ChatEventRefV1{Version: 1, MessageID: mid, RoomID: rid, MemberID: v.member, JoinedAt: v.joined})
		if e != nil {
			return false, e
		}
		if e = appendEvent(ctx, tx, v.account, r.app, "chat-live-v1:"+mid, chatLiveMarker, "chat", &rid, data, at); e != nil {
			return false, e
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.receipts(source,source_id,app_id,processed_at)VALUES('chat-live-v1',$1,$2,clock_timestamp())`, mid, r.app); e != nil {
		return false, e
	}
	return true, tx.Commit()
}

func (r *Repository) projectChatReadEvent(ctx context.Context, tx *sql.Tx) (bool, error) {
	var eventID, memberID, rid, owner, excluded string
	var lastRead, at time.Time
	e := tx.QueryRowContext(ctx, `SELECT e.id::text,e.member_id::text,e.room_id::text,e.account_id::text,e.last_read_at,e.excluded_device_id,e.created_at FROM owned_chat_read_events e WHERE NOT EXISTS(SELECT 1 FROM notification_v1.receipts p WHERE p.source='chat-read-v1' AND p.source_id=e.id AND p.app_id=$1) ORDER BY e.created_at,e.id LIMIT 1`, r.app).Scan(&eventID, &memberID, &rid, &owner, &lastRead, &excluded, &at)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.account_id::text,m.id::text,m.joined_at FROM chat_members m JOIN chat_rooms room ON room.id=m.chat_room_id WHERE m.chat_room_id=$1 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND room.deleted_at IS NULL AND(m.account_id=$2 OR(room.owned_read_receipts_public AND EXISTS(SELECT 1 FROM owned_chat_subscriptions s JOIN owned_chat_connections c ON c.id=s.connection_id WHERE s.member_id=m.id AND s.joined_at=m.joined_at AND s.expires_at>clock_timestamp() AND c.account_id=m.account_id AND c.closed_at IS NULL AND c.expires_at>clock_timestamp()))) ORDER BY m.account_id`, rid, owner)
	if e != nil {
		return false, e
	}
	type recipient struct {
		account, member string
		joined          time.Time
	}
	recipients := []recipient{}
	for rows.Next() {
		var v recipient
		if e = rows.Scan(&v.account, &v.member, &v.joined); e != nil {
			rows.Close()
			return false, e
		}
		recipients = append(recipients, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, v := range recipients {
		active, x := lockAccount(ctx, tx, v.account)
		if x != nil {
			return false, x
		}
		if !active {
			continue
		}
		data, x := json.Marshal(notifications.ChatEventRefV1{Version: 1, Kind: "read", ReceiptID: eventID, RoomID: rid, MemberID: v.member, JoinedAt: v.joined, ExcludedDevice: excluded})
		if x != nil {
			return false, x
		}
		if x = appendEvent(ctx, tx, v.account, r.app, "chat-read-v1:"+eventID, chatLiveMarker, "chat", &rid, data, at); x != nil {
			return false, x
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.receipts(source,source_id,app_id,processed_at)VALUES('chat-read-v1',$1,$2,clock_timestamp())`, eventID, r.app)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}

func (r *Repository) projectChatReference(ctx context.Context, tx *sql.Tx, actor string, event notifications.Event) (notifications.Event, bool, error) {
	if r.chatEvents == nil {
		return notifications.Event{}, false, nil
	}
	var ref notifications.ChatEventRefV1
	if json.Unmarshal(event.Data, &ref) != nil || ref.Version != 1 {
		return notifications.Event{}, false, notifications.ErrUnavailable
	}
	fresh, allow, e := r.chatEvents.ProjectChatEvent(ctx, tx, actor, ref)
	fresh.Sequence = event.Sequence
	return fresh, allow, e
}
