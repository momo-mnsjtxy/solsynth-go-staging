// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
)

const pinColumns = `id::text,message_id::text,room_id::text,pinned_by_member_id::text,expires_at,created_at,updated_at,deleted_at`

func scanPin(s scanner) (v rooms.Pin, e error) {
	e = s.Scan(&v.ID, &v.MessageID, &v.ChatRoomID, &v.PinnedByMemberID, &v.ExpiresAt, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	return v, e
}
func (o *Organization) pinMember(ctx context.Context, tx *sql.Tx, actor string, room rooms.Room) (rooms.Member, error) {
	m, e := requireMember(ctx, tx, room.ID, actor)
	if e != nil {
		return m, e
	}
	if e = o.r.readable(ctx, tx, room, actor); e != nil {
		return m, e
	}
	if room.RealmID != nil {
		if o.r.social == nil {
			return m, rooms.ErrUnavailable
		}
		role, x := o.r.social.RealmRoleForWriteInTransaction(ctx, tx, actor, *room.RealmID)
		if x != nil || role < social.Moderator {
			if x != nil && !errors.Is(x, social.ErrForbidden) && !errors.Is(x, social.ErrNotFound) {
				return m, x
			}
			return m, rooms.ErrForbidden
		}
	} else if room.Type != 1 && room.AccountID != actor {
		return m, rooms.ErrForbidden
	}
	return m, nil
}
func (o *Organization) pinProjection(ctx context.Context, tx *sql.Tx, v *rooms.Pin) error {
	// Never expose another member's private read cursor through the pin author.
	m, e := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM chat_members WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, v.PinnedByMemberID, v.ChatRoomID))
	if errors.Is(e, rooms.ErrNotFound) {
		v.PinnedBy = nil
		return nil
	}
	if e != nil {
		return e
	}
	m.LastReadAt = nil
	m.TimeoutCause = nil
	m.TimeoutUntil = nil
	m.Account, e = canonicalAccount(ctx, tx, m.AccountID)
	if errors.Is(e, sql.ErrNoRows) {
		m.Account = nil
		e = nil
	}
	if e != nil {
		return e
	}
	v.PinnedBy = &m
	return nil
}
func (o *Organization) Pin(ctx context.Context, actor, rid string, q rooms.PinRequest) (rooms.Pin, error) {
	var out rooms.Pin
	if e := o.valid(actor); e != nil {
		return out, e
	}
	if valid(rid, q.MessageID) != nil || q.ExpiresAt != nil && (q.ExpiresAt.Year() < 1 || q.ExpiresAt.Year() > 9999) {
		return out, rooms.ErrInvalid
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, rid, "chat.pins.manage", "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	room, e := o.r.room(ctx, tx, rid, false)
	if e != nil {
		return out, e
	}
	m, e := o.pinMember(ctx, tx, actor, room)
	if e != nil {
		return out, e
	}
	var visible, existing bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages msg JOIN chat_members sender ON sender.id=msg.sender_id AND sender.chat_room_id=msg.chat_room_id WHERE msg.id=$1 AND msg.chat_room_id=$2 AND msg.deleted_at IS NULL AND sender.deleted_at IS NULL),EXISTS(SELECT 1 FROM owned_chat_pins WHERE room_id=$2 AND message_id=$1 AND deleted_at IS NULL)`, q.MessageID, rid).Scan(&visible, &existing)
	if e != nil {
		return out, e
	}
	if !visible || existing {
		return out, rooms.ErrPinFailed
	}
	pid, e := id()
	if e != nil {
		return out, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	out, e = scanPin(tx.QueryRowContext(ctx, `INSERT INTO owned_chat_pins(id,room_id,message_id,pinned_by_member_id,expires_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$6)RETURNING `+pinColumns, pid, rid, q.MessageID, m.ID, q.ExpiresAt, now))
	if e != nil {
		return rooms.Pin{}, e
	}
	meta := map[string]any{"pin_id": pid, "message_id": q.MessageID, "pinned_by_member_id": m.ID}
	if q.ExpiresAt != nil {
		meta["expires_at"] = q.ExpiresAt.UnixMilli()
	}
	raw, e := json.Marshal(meta)
	if e != nil {
		return rooms.Pin{}, e
	}
	if _, e = appendMessage(ctx, tx, rid, m.ID, "messages.pinned", nil, nil, nil, raw, nil, nil); e != nil {
		return rooms.Pin{}, e
	}
	if e = o.pinProjection(ctx, tx, &out); e != nil {
		return rooms.Pin{}, e
	}
	if _, e = o.pinMember(ctx, tx, actor, room); e != nil {
		return rooms.Pin{}, e
	}
	if e = o.r.commit(ctx, tx, actor, "chat.pins.manage"); e != nil {
		return rooms.Pin{}, e
	}
	return out, nil
}
func (o *Organization) Unpin(ctx context.Context, actor, rid, pid string) error {
	if e := o.valid(actor); e != nil {
		return e
	}
	if valid(rid, pid) != nil {
		return rooms.ErrInvalid
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, rid, "chat.pins.manage", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	room, e := o.r.room(ctx, tx, rid, false)
	if e != nil {
		return e
	}
	m, e := o.pinMember(ctx, tx, actor, room)
	if e != nil {
		return e
	}
	pin, e := scanPin(tx.QueryRowContext(ctx, `UPDATE owned_chat_pins SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND room_id=$2 AND deleted_at IS NULL RETURNING `+pinColumns, pid, rid))
	if errors.Is(e, sql.ErrNoRows) {
		return rooms.ErrUnpinFailed
	}
	if e != nil {
		return e
	}
	raw, e := json.Marshal(map[string]any{"pin_id": pin.ID, "message_id": pin.MessageID})
	if e != nil {
		return e
	}
	if _, e = appendMessage(ctx, tx, rid, m.ID, "messages.unpinned", nil, nil, nil, raw, nil, nil); e != nil {
		return e
	}
	if _, e = o.pinMember(ctx, tx, actor, room); e != nil {
		return e
	}
	return o.r.commit(ctx, tx, actor, "chat.pins.manage")
}
func (o *Organization) Pins(ctx context.Context, actor, rid string, expired bool) ([]rooms.Pin, error) {
	if o == nil || o.r == nil {
		return nil, rooms.ErrUnavailable
	}
	if valid(rid) != nil || actor != "" && !rooms.ValidID(actor) {
		return nil, rooms.ErrInvalid
	}
	var tx *sql.Tx
	var e error
	if actor == "" {
		tx, e = o.r.begin(ctx, false)
	} else {
		tx, e = o.r.beginRoomWrite(ctx, actor, rid, "", "")
	}
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	room, e := o.r.room(ctx, tx, rid, true)
	if e != nil {
		return nil, e
	}
	if e = o.r.readable(ctx, tx, room, actor); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+pinColumns+` FROM owned_chat_pins pin WHERE room_id=$1 AND deleted_at IS NULL AND($2::boolean OR expires_at IS NULL OR expires_at>clock_timestamp()) AND EXISTS(SELECT 1 FROM chat_messages msg JOIN chat_members sender ON sender.id=msg.sender_id AND sender.chat_room_id=msg.chat_room_id WHERE msg.id=pin.message_id AND msg.chat_room_id=pin.room_id AND msg.deleted_at IS NULL AND sender.deleted_at IS NULL) ORDER BY created_at DESC,id DESC LIMIT 1001`, rid, expired)
	if e != nil {
		return nil, e
	}
	out := []rooms.Pin{}
	for rows.Next() {
		p, x := scanPin(rows)
		if x != nil {
			rows.Close()
			return nil, x
		}
		out = append(out, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(out) > 1000 {
		return nil, rooms.ErrUnavailable
	}
	for i := range out {
		if e = o.pinProjection(ctx, tx, &out[i]); e != nil {
			return nil, e
		}
	}
	if e = o.r.readable(ctx, tx, room, actor); e != nil {
		return nil, e
	}
	if actor == "" {
		e = tx.Commit()
	} else {
		e = o.r.commit(ctx, tx, actor, "")
	}
	if e != nil {
		return nil, e
	}
	return out, nil
}
