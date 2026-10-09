// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"math"
	"time"

	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func (o *Organization) Summary(ctx context.Context, actor string) (map[string]rooms.ChatSummary, error) {
	if e := o.valid(actor); e != nil {
		return nil, e
	}
	if o.reader == nil {
		return nil, rooms.ErrUnavailable
	}
	tx, ids, e := o.beginAll(ctx, actor, "")
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	out, e := o.summaryInTransaction(ctx, tx, actor, ids)
	if e != nil {
		return nil, e
	}
	if e = o.commitAll(ctx, tx, actor, "", ids); e != nil {
		return nil, e
	}
	return out, nil
}
func (o *Organization) summaryInTransaction(ctx context.Context, tx *sql.Tx, actor string, ids []string) (map[string]rooms.ChatSummary, error) {
	out := map[string]rooms.ChatSummary{}
	rows, e := tx.QueryContext(ctx, `SELECT member.chat_room_id::text,count(msg.id) FILTER(WHERE member.notify=$3 OR(member.notify=$4 AND msg.members_mentioned @> jsonb_build_array($1::text))),count(msg.id) FROM chat_members member LEFT JOIN chat_messages msg ON msg.chat_room_id=member.chat_room_id AND msg.sender_id<>member.id AND msg.type<>'placeholder' AND left(msg.type,9)<>'messages.' AND msg.deleted_at IS NULL AND(member.last_read_at IS NULL OR msg.created_at>member.last_read_at) AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=msg.sender_id AND sender.chat_room_id=msg.chat_room_id AND sender.deleted_at IS NULL) WHERE member.account_id=$1::uuid AND member.chat_room_id=ANY($2::uuid[]) AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned GROUP BY member.chat_room_id`, actor, ids, o.r.policy.NotifyAll, o.r.policy.NotifyMentions)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var rid string
		var count, raw int64
		if e = rows.Scan(&rid, &count, &raw); e != nil {
			rows.Close()
			return nil, e
		}
		if count < 0 || raw < count || raw > math.MaxInt32 {
			rows.Close()
			return nil, messager.ErrUnreadCountOverflow
		}
		out[rid] = rooms.ChatSummary{UnreadCount: int32(count), HasUnread: raw > 0}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	// Deleted messages are excluded even though the legacy helper ignored global
	// filters. A deleted last row must never resurrect its content through summary.
	rows, e = tx.QueryContext(ctx, `SELECT DISTINCT ON(m.chat_room_id) m.id::text FROM chat_messages m WHERE m.chat_room_id=ANY($1::uuid[]) AND m.deleted_at IS NULL AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=m.sender_id AND sender.chat_room_id=m.chat_room_id AND sender.deleted_at IS NULL) ORDER BY m.chat_room_id,m.created_at DESC,m.id DESC`, ids)
	if e != nil {
		return nil, e
	}
	mids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		mids = append(mids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	messages, e := o.reader.OrganizationMessages(ctx, o.r.db, tx, actor, mids)
	if e != nil {
		return nil, e
	}
	for i := range messages {
		v := out[messages[i].ChatRoomID]
		v.LastMessage = &messages[i]
		out[messages[i].ChatRoomID] = v
	}
	return out, nil
}
func (o *Organization) ReadAll(ctx context.Context, actor string) error {
	tx, ids, e := o.beginAll(ctx, actor, "chat.read.all")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// The room locks serialize sequence allocation. Include any source time that
	// has advanced beyond the wall clock; future sends remain strictly later.
	now, e := timestamp(ctx, tx)
	if e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `WITH changed AS(UPDATE chat_members member SET last_read_at=greatest(member.last_read_at,$3::timestamptz,(SELECT max(m.created_at) FROM chat_messages m WHERE m.chat_room_id=member.chat_room_id AND m.deleted_at IS NULL)),owned_updated_at=clock_timestamp() WHERE member.account_id=$1::uuid AND member.chat_room_id=ANY($2::uuid[]) AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned RETURNING id,chat_room_id,account_id,last_read_at),saved AS(INSERT INTO owned_chat_read_receipts(member_id,room_id,account_id,last_read_at,updated_at)SELECT id,chat_room_id,account_id,last_read_at,clock_timestamp() FROM changed ON CONFLICT(member_id)DO UPDATE SET last_read_at=greatest(owned_chat_read_receipts.last_read_at,EXCLUDED.last_read_at),updated_at=EXCLUDED.updated_at RETURNING member_id)SELECT id::text,chat_room_id::text,account_id::text,last_read_at FROM changed`, actor, ids, now)
	if e != nil {
		return e
	}
	type receipt struct {
		member, room, account string
		at                    time.Time
	}
	changed := []receipt{}
	for rows.Next() {
		var v receipt
		if e = rows.Scan(&v.member, &v.room, &v.account, &v.at); e != nil {
			rows.Close()
			return e
		}
		changed = append(changed, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range changed {
		eventID, x := id()
		if x != nil {
			return x
		}
		if _, x = tx.ExecContext(ctx, `INSERT INTO owned_chat_read_events(id,member_id,room_id,account_id,last_read_at,excluded_device_id,created_at)VALUES($1,$2,$3,$4,$5,$6,clock_timestamp())`, eventID, v.member, v.room, v.account, v.at, rooms.MLSWriteFrom(ctx).Device); x != nil {
			return x
		}
	}
	return o.commitAll(ctx, tx, actor, "chat.read.all", ids)
}
func (o *Organization) Search(ctx context.Context, actor string, q rooms.SearchRequest) (rooms.SearchPage, error) {
	out := rooms.SearchPage{Rooms: []rooms.SearchRoom{}}
	if e := o.valid(actor); e != nil {
		return out, e
	}
	var e error
	q, e = rooms.ValidateSearch(q)
	if e != nil {
		return out, e
	}
	if o.reader == nil || o.entitlement == nil {
		return out, rooms.ErrUnavailable
	}
	tx, ids, e := o.beginAll(ctx, actor, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	if e = o.entitlement.RequireChatSearch(ctx, o.r.db, tx, actor, now); e != nil {
		return out, e
	}
	selected := ids
	if len(q.RoomIDs) > 0 {
		selected = []string{}
		for _, id := range ids {
			if hasID(q.RoomIDs, id) {
				selected = append(selected, id)
			}
		}
	}
	// strpos is deliberately literal: %, _ and backslash are search text, never
	// wildcard syntax. Sender matches current canonical UUID/name/nick only.
	where := `m.chat_room_id=ANY($1::uuid[]) AND m.deleted_at IS NULL AND m.type='text' AND(m.encryption_meta IS NULL OR m.encryption_meta='null'::jsonb) AND m.content IS NOT NULL AND strpos(lower(m.content),lower($2::text))>0 AND($3::timestamptz IS NULL OR m.created_at>=$3::timestamptz) AND($4::timestamptz IS NULL OR m.created_at<$4::timestamptz) AND EXISTS(SELECT 1 FROM chat_rooms room WHERE room.id=m.chat_room_id AND room.encryption_mode=$6) AND EXISTS(SELECT 1 FROM chat_members sender JOIN owned_authority_accounts a ON a.id=sender.account_id WHERE sender.id=m.sender_id AND sender.chat_room_id=m.chat_room_id AND sender.deleted_at IS NULL AND NOT a.disabled AND($5::text='' OR a.id::text=$5 OR strpos(lower(a.name),lower($5::text))>0 OR strpos(lower(a.nick),lower($5::text))>0))`
	args := []any{selected, q.Query, q.After, q.Before, q.Sender, o.r.policy.NoneEncryptionMode}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM chat_messages m WHERE `+where, args...).Scan(&out.Total); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.id::text FROM chat_messages m WHERE `+where+` ORDER BY m.created_at DESC,m.id DESC LIMIT $7 OFFSET $8`, append(args, q.Take, q.Offset)...)
	if e != nil {
		return out, e
	}
	mids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		mids = append(mids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	messages, e := o.reader.OrganizationMessages(ctx, o.r.db, tx, actor, mids)
	if e != nil {
		return out, e
	}
	index := map[string]int{}
	for _, m := range messages {
		idx, ok := index[m.ChatRoomID]
		if !ok {
			room, x := o.r.room(ctx, tx, m.ChatRoomID, false)
			if x != nil {
				return rooms.SearchPage{}, x
			}
			if x = o.r.readable(ctx, tx, room, actor); x != nil {
				return rooms.SearchPage{}, x
			}
			if x = o.r.decorateRoom(ctx, tx, &room, actor); x != nil {
				return rooms.SearchPage{}, x
			}
			idx = len(out.Rooms)
			index[m.ChatRoomID] = idx
			out.Rooms = append(out.Rooms, rooms.SearchRoom{Room: room, Messages: []messager.MessageProjection{}})
		}
		out.Rooms[idx].Messages = append(out.Rooms[idx].Messages, m)
	}
	now, e = timestamp(ctx, tx)
	if e != nil {
		return rooms.SearchPage{}, e
	}
	if e = o.entitlement.RequireChatSearch(ctx, o.r.db, tx, actor, now); e != nil {
		return rooms.SearchPage{}, e
	}
	if e = o.commitAll(ctx, tx, actor, "", ids); e != nil {
		return rooms.SearchPage{}, e
	}
	return out, nil
}
