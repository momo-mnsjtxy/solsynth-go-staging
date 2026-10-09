// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"sort"
	"time"
)

var _ rooms.SubscriptionSyncRepository = (*Organization)(nil)

// Millisecond buckets match the authored Flutter cursor precision. Only closed
// buckets are emitted, preventing concurrent same-millisecond inserts from
// falling behind a cursor. Both page selection and ordering use this precision.
const syncMillis = "date_trunc('milliseconds',m.created_at)"

func (o *Organization) GlobalSync(ctx context.Context, actor string, q rooms.GlobalSyncRequest) (rooms.GlobalSyncResponse, error) {
	out := rooms.GlobalSyncResponse{Messages: []messager.MessageProjection{}}
	if e := rooms.ValidateGlobalSync(q); e != nil {
		return out, e
	}
	if o == nil || o.reader == nil {
		return out, rooms.ErrUnavailable
	}
	tx, ids, e := o.beginAll(ctx, actor, "chat.sync")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	end := now.Truncate(time.Millisecond)
	last := time.UnixMilli(q.LastSyncTimestamp).UTC()
	if !last.Before(end) {
		return out, rooms.ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.id::text FROM chat_messages m WHERE m.chat_room_id=ANY($1::uuid[]) AND m.thread_id IS NULL AND m.deleted_at IS NULL AND m.created_at<$4 AND (`+syncMillis+`>$2 OR (`+syncMillis+`=$2 AND $3::uuid IS NOT NULL AND m.id>$3::uuid)) AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=m.sender_id AND sender.chat_room_id=m.chat_room_id AND sender.deleted_at IS NULL) ORDER BY `+syncMillis+`,m.id LIMIT 500`, ids, last, q.LastSyncMessageID, end)
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
	out.Messages, e = o.reader.OrganizationMessages(ctx, o.r.db, tx, actor, mids)
	if e != nil {
		return rooms.GlobalSyncResponse{}, e
	}
	sort.Slice(out.Messages, func(i, j int) bool {
		a, b := out.Messages[i], out.Messages[j]
		am, bm := a.CreatedAt.UnixMilli(), b.CreatedAt.UnixMilli()
		if am != bm {
			return am < bm
		}
		return a.ID < b.ID
	})
	out.TotalCount = len(out.Messages)
	out.CurrentTimestamp = end.Add(-time.Millisecond)
	if len(out.Messages) > 0 {
		m := out.Messages[len(out.Messages)-1]
		out.CurrentTimestamp = m.CreatedAt.Truncate(time.Millisecond)
		out.CurrentMessageID = &m.ID
	}
	if e = o.commitAll(ctx, tx, actor, "chat.sync", ids); e != nil {
		return rooms.GlobalSyncResponse{}, e
	}
	return out, nil
}

type roomSyncCandidate struct {
	id                          string
	memberAt, roomAt, summaryAt time.Time
	active                      bool
	joined                      *time.Time
}

// A timestamp-only route cannot split an equal-time bucket safely. Keep all
// entries at its selected boundary; explicit overflow never advances the cursor.
func syncBoundary(values []time.Time, end time.Time) (time.Time, error) {
	if len(values) > 10000 {
		return time.Time{}, rooms.ErrSyncBucketOverflow
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Before(values[j]) })
	if len(values) >= 500 && values[499].Before(end) {
		return values[499], nil
	}
	return end, nil
}
func (o *Organization) JoinedRoomSync(ctx context.Context, actor string, q rooms.JoinedRoomSyncRequest) (rooms.JoinedRoomSyncResponse, error) {
	out := rooms.JoinedRoomSyncResponse{Changes: []rooms.JoinedRoomChange{}, Summaries: []rooms.JoinedRoomSummary{}, Groups: []rooms.ChatGroup{}}
	if !rooms.ValidSyncTimestamp(q.LastSyncTimestamp) {
		return out, rooms.ErrInvalid
	}
	if o == nil || o.reader == nil {
		return out, rooms.ErrUnavailable
	}
	tx, ids, e := o.beginAll(ctx, actor, "chat.sync")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	end := now.Truncate(time.Millisecond).Add(-time.Millisecond)
	last := time.UnixMilli(q.LastSyncTimestamp).UTC()
	if last.After(end) {
		return out, rooms.ErrInvalid
	}
	// Membership tombstones are read only for the current account. No room or
	// member object accompanies a removed/inaccessible room.
	rows, e := tx.QueryContext(ctx, `SELECT member.chat_room_id::text,date_trunc('milliseconds',greatest(member.owned_updated_at,member.joined_at,member.leave_at,member.deleted_at)),date_trunc('milliseconds',greatest(room.owned_created_at,room.owned_updated_at,room.deleted_at)),member.joined_at FROM chat_members member JOIN chat_rooms room ON room.id=member.chat_room_id WHERE member.account_id=$1 AND ($2::bigint<>0 OR(member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned)) AND (greatest(member.owned_updated_at,member.joined_at,member.leave_at,member.deleted_at)>$3 OR (member.chat_room_id=ANY($4::uuid[]) AND greatest(room.owned_created_at,room.owned_updated_at,room.deleted_at)>$3)) ORDER BY member.chat_room_id LIMIT 10001`, actor, q.LastSyncTimestamp, last, ids)
	if e != nil {
		return out, e
	}
	candidates := map[string]*roomSyncCandidate{}
	changeTimes := []time.Time{}
	for rows.Next() {
		v := roomSyncCandidate{}
		if e = rows.Scan(&v.id, &v.memberAt, &v.roomAt, &v.joined); e != nil {
			rows.Close()
			return out, e
		}
		v.active = hasID(ids, v.id)
		candidates[v.id] = &v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(candidates) > 10000 {
		return out, rooms.ErrSyncBucketOverflow
	}
	for _, v := range candidates {
		t := v.memberAt
		if v.active && v.roomAt.After(t) {
			t = v.roomAt
		}
		if t.After(last) && !t.After(end) {
			changeTimes = append(changeTimes, t)
			if v.active {
				v.summaryAt = t
			}
		}
	}
	rows, e = tx.QueryContext(ctx, `SELECT m.chat_room_id::text,date_trunc('milliseconds',max(greatest(m.created_at,m.updated_at,m.deleted_at))) FROM chat_messages m WHERE m.chat_room_id=ANY($1::uuid[]) GROUP BY m.chat_room_id`, ids)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var rid string
		var t time.Time
		if e = rows.Scan(&rid, &t); e != nil {
			rows.Close()
			return out, e
		}
		v := candidates[rid]
		if v == nil {
			v = &roomSyncCandidate{id: rid, active: true}
			candidates[rid] = v
		}
		if t.After(v.summaryAt) && t.After(last) && !t.After(end) {
			v.summaryAt = t
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	summaryTimes := []time.Time{}
	for _, v := range candidates {
		if v.summaryAt.After(last) && !v.summaryAt.After(end) {
			summaryTimes = append(summaryTimes, v.summaryAt)
		}
	}
	horizon, e := syncBoundary(changeTimes, end)
	if e != nil {
		return out, e
	}
	summaryHorizon, e := syncBoundary(summaryTimes, end)
	if e != nil {
		return out, e
	}
	if summaryHorizon.Before(horizon) {
		horizon = summaryHorizon
	}
	summaries, e := o.summaryInTransaction(ctx, tx, actor, ids)
	if e != nil {
		return out, e
	}
	for _, v := range candidates {
		t := v.memberAt
		if v.active && v.roomAt.After(t) {
			t = v.roomAt
		}
		if t.After(last) && !t.After(horizon) {
			c := rooms.JoinedRoomChange{RoomID: v.id, Type: "removed", ChangedAt: t}
			if v.active {
				room, x := o.r.detailRoom(ctx, tx, actor, v.id, false)
				if x != nil {
					return out, x
				}
				if x = o.r.decorateRoom(ctx, tx, &room, actor); x != nil {
					return out, x
				}
				member, x := requireMember(ctx, tx, v.id, actor)
				if x != nil {
					return out, x
				}
				member.Account, x = canonicalAccount(ctx, tx, actor)
				if x != nil {
					return out, x
				}
				c.Room = &room
				c.Member = &member
				c.Type = "updated"
				if v.joined != nil && v.joined.After(last) {
					c.Type = "joined"
				}
			}
			out.Changes = append(out.Changes, c)
		}
		if v.active && v.summaryAt.After(last) && !v.summaryAt.After(horizon) {
			out.Summaries = append(out.Summaries, rooms.JoinedRoomSummary{RoomID: v.id, ChatSummary: summaries[v.id], ChangedAt: v.summaryAt})
		}
	}
	sort.Slice(out.Changes, func(i, j int) bool {
		a, b := out.Changes[i], out.Changes[j]
		if !a.ChangedAt.Equal(b.ChangedAt) {
			return a.ChangedAt.Before(b.ChangedAt)
		}
		return a.RoomID < b.RoomID
	})
	sort.Slice(out.Summaries, func(i, j int) bool {
		a, b := out.Summaries[i], out.Summaries[j]
		if !a.ChangedAt.Equal(b.ChangedAt) {
			return a.ChangedAt.Before(b.ChangedAt)
		}
		return a.RoomID < b.RoomID
	})
	out.Groups, e = o.syncGroups(ctx, tx, actor)
	if e != nil {
		return out, e
	}
	out.CurrentTimestamp = horizon
	out.TotalCount = len(out.Changes)
	out.SummaryTotalCount = len(out.Summaries)
	if e = o.commitAll(ctx, tx, actor, "chat.sync", ids); e != nil {
		return rooms.JoinedRoomSyncResponse{}, e
	}
	return out, nil
}
func (o *Organization) syncGroups(ctx context.Context, tx *sql.Tx, actor string) ([]rooms.ChatGroup, error) {
	rows, e := tx.QueryContext(ctx, `SELECT `+groupColumns+` FROM owned_chat_groups WHERE account_id=$1 AND deleted_at IS NULL ORDER BY sort_order,created_at,id LIMIT 1001`, actor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []rooms.ChatGroup{}
	for rows.Next() {
		v, e := scanGroup(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(out) > 1000 {
		return nil, rooms.ErrSyncBucketOverflow
	}
	return out, nil
}
