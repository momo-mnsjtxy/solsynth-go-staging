// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"solsynth.local/go-migration/ports/messager/rooms"
)

const groupColumns = `id::text,account_id::text,name,color,icon,sort_order,created_at,updated_at,deleted_at`

func scanGroup(s scanner) (v rooms.ChatGroup, e error) {
	e = s.Scan(&v.ID, &v.AccountID, &v.Name, &v.Color, &v.Icon, &v.Order, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	return v, organizationNotFound(e)
}
func (o *Organization) group(ctx context.Context, tx *sql.Tx, actor, gid string) (rooms.ChatGroup, error) {
	return scanGroup(tx.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM owned_chat_groups WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, gid, actor))
}
func (o *Organization) Groups(ctx context.Context, actor string) ([]rooms.ChatGroup, error) {
	if e := o.valid(actor); e != nil {
		return nil, e
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, "", "", "")
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+groupColumns+` FROM owned_chat_groups WHERE account_id=$1 AND deleted_at IS NULL ORDER BY sort_order,created_at,id LIMIT 1001`, actor)
	if e != nil {
		return nil, e
	}
	out := []rooms.ChatGroup{}
	for rows.Next() {
		v, x := scanGroup(rows)
		if x != nil {
			rows.Close()
			return nil, x
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(out) > 1000 {
		return nil, rooms.ErrUnavailable
	}
	if e = o.r.commit(ctx, tx, actor, ""); e != nil {
		return nil, e
	}
	return out, nil
}
func (o *Organization) CreateGroup(ctx context.Context, actor string, q rooms.GroupPatch) (rooms.ChatGroup, error) {
	if e := o.valid(actor); e != nil {
		return rooms.ChatGroup{}, e
	}
	if e := rooms.ValidateGroup(q, true); e != nil {
		return rooms.ChatGroup{}, e
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, "", "chat.groups.manage", "")
	if e != nil {
		return rooms.ChatGroup{}, e
	}
	defer tx.Rollback()
	var count int
	var order int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(max(sort_order)::bigint+1,0) FROM owned_chat_groups WHERE account_id=$1 AND deleted_at IS NULL`, actor).Scan(&count, &order); e != nil {
		return rooms.ChatGroup{}, e
	}
	if count >= 1000 {
		return rooms.ChatGroup{}, rooms.ErrConflict
	}
	if q.Order != nil {
		order = int64(*q.Order)
	}
	if order > math.MaxInt32 {
		return rooms.ChatGroup{}, rooms.ErrConflict
	}
	gid, e := id()
	if e != nil {
		return rooms.ChatGroup{}, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return rooms.ChatGroup{}, e
	}
	v, e := scanGroup(tx.QueryRowContext(ctx, `INSERT INTO owned_chat_groups(id,account_id,name,color,icon,sort_order,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$7) RETURNING `+groupColumns, gid, actor, *q.Name, q.Color, q.Icon, order, now))
	if e != nil {
		return rooms.ChatGroup{}, e
	}
	if e = o.r.commit(ctx, tx, actor, "chat.groups.manage"); e != nil {
		return rooms.ChatGroup{}, e
	}
	return v, nil
}
func (o *Organization) UpdateGroup(ctx context.Context, actor, gid string, q rooms.GroupPatch) (rooms.ChatGroup, error) {
	if e := o.valid(actor); e != nil {
		return rooms.ChatGroup{}, e
	}
	if !rooms.ValidID(gid) {
		return rooms.ChatGroup{}, rooms.ErrInvalid
	}
	if e := rooms.ValidateGroup(q, false); e != nil {
		return rooms.ChatGroup{}, e
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, "", "", "")
	if e != nil {
		return rooms.ChatGroup{}, e
	}
	defer tx.Rollback()
	v, e := scanGroup(tx.QueryRowContext(ctx, `UPDATE owned_chat_groups SET name=coalesce($3,name),color=coalesce($4,color),icon=coalesce($5,icon),sort_order=coalesce($6,sort_order),updated_at=clock_timestamp() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL RETURNING `+groupColumns, gid, actor, q.Name, q.Color, q.Icon, q.Order))
	if e != nil {
		return rooms.ChatGroup{}, e
	}
	if e = o.r.commit(ctx, tx, actor, ""); e != nil {
		return rooms.ChatGroup{}, e
	}
	return v, nil
}
func (o *Organization) DeleteGroup(ctx context.Context, actor, gid string) error {
	if e := o.valid(actor); e != nil {
		return e
	}
	if !rooms.ValidID(gid) {
		return rooms.ErrInvalid
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, "", "", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = o.group(ctx, tx, actor, gid); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_chat_group_members WHERE group_id=$1 AND account_id=$2`, gid, actor); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_chat_groups SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, gid, actor); e != nil {
		return e
	}
	return o.r.commit(ctx, tx, actor, "")
}
func (o *Organization) MoveToGroup(ctx context.Context, actor, rid string, gid *string) error {
	if e := o.valid(actor); e != nil {
		return e
	}
	if !rooms.ValidID(rid) || gid != nil && !rooms.ValidID(*gid) {
		return rooms.ErrInvalid
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, rid, "", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	room, e := o.r.room(ctx, tx, rid, false)
	if e != nil {
		return e
	}
	if e = o.r.readable(ctx, tx, room, actor); e != nil {
		return e
	}
	m, e := requireMember(ctx, tx, rid, actor)
	if e != nil {
		return e
	}
	if gid == nil {
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_chat_group_members WHERE member_id=$1 AND account_id=$2`, m.ID, actor)
	} else {
		if _, e = o.group(ctx, tx, actor, *gid); e != nil {
			if errors.Is(e, rooms.ErrNotFound) {
				return rooms.ErrGroupMove
			}
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_group_members(member_id,account_id,group_id,updated_at)VALUES($1,$2,$3,clock_timestamp()) ON CONFLICT(member_id)DO UPDATE SET group_id=EXCLUDED.group_id,updated_at=EXCLUDED.updated_at WHERE owned_chat_group_members.account_id=EXCLUDED.account_id`, m.ID, actor, *gid)
	}
	if e != nil {
		return e
	}
	return o.r.commit(ctx, tx, actor, "")
}
func (o *Organization) GroupMember(ctx context.Context, actor, rid string) (rooms.GroupMember, error) {
	var out rooms.GroupMember
	if e := o.valid(actor); e != nil {
		return out, e
	}
	if !rooms.ValidID(rid) {
		return out, rooms.ErrInvalid
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, rid, "", "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	room, e := o.r.room(ctx, tx, rid, false)
	if e != nil {
		return out, e
	}
	if e = o.r.readable(ctx, tx, room, actor); e != nil {
		return out, e
	}
	out.Member, e = requireMember(ctx, tx, rid, actor)
	if e != nil {
		return out, e
	}
	var gid string
	e = tx.QueryRowContext(ctx, `SELECT binding.group_id::text FROM owned_chat_group_members binding JOIN owned_chat_groups g ON g.id=binding.group_id AND g.account_id=binding.account_id WHERE binding.member_id=$1 AND binding.account_id=$2 AND g.deleted_at IS NULL`, out.ID, actor).Scan(&gid)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if e == nil {
		g, x := o.group(ctx, tx, actor, gid)
		if x != nil {
			return out, x
		}
		out.ChatGroupID = &gid
		out.ChatGroup = &g
	}
	if e = o.r.commit(ctx, tx, actor, ""); e != nil {
		return rooms.GroupMember{}, e
	}
	return out, nil
}
