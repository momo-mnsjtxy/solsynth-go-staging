// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
)

func (r *Repository) Members(ctx context.Context, actor, rid string, take, offset int) ([]rooms.Member, error) {
	if e := valid(actor, rid); e != nil {
		return nil, e
	}
	if e := page(take, offset); e != nil {
		return nil, e
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, false)
	if e != nil {
		return nil, e
	}
	// Membership lists require active membership even for public rooms. Public
	// message access does not grant access to pending invitations or read receipts.
	if _, e = requireMember(ctx, tx, rid, actor); e != nil {
		return nil, e
	}
	if e = r.readable(ctx, tx, v, actor); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+memberColumns+` FROM chat_members WHERE chat_room_id=$1 AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned AND (joined_at IS NOT NULL OR $2) ORDER BY joined_at NULLS LAST,id LIMIT $3 OFFSET $4`, rid, v.AccountID == actor, take, offset)
	if e != nil {
		return nil, e
	}
	out := []rooms.Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if m.AccountID != actor {
			m.LastReadAt = nil
		}
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) Me(ctx context.Context, actor, rid string) (rooms.Member, error) {
	if e := valid(actor, rid); e != nil {
		return rooms.Member{}, e
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return rooms.Member{}, e
	}
	defer tx.Rollback()
	v, x := r.room(ctx, tx, rid, false)
	if x != nil {
		e = x
		return rooms.Member{}, e
	}
	if e = r.readable(ctx, tx, v, actor); e != nil {
		return rooms.Member{}, e
	}
	m, e := requireMember(ctx, tx, rid, actor)
	if e != nil {
		return m, e
	}
	return m, tx.Commit()
}
func (r *Repository) Invites(ctx context.Context, actor string, take, offset int) ([]rooms.Member, error) {
	if e := valid(actor); e != nil {
		return nil, e
	}
	if e := page(take, offset); e != nil {
		return nil, e
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+memberColumns+` FROM chat_members WHERE account_id=$1 AND joined_at IS NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned AND EXISTS(SELECT 1 FROM chat_rooms r WHERE r.id=chat_room_id AND r.deleted_at IS NULL AND r.owned_owner_id IS NOT NULL AND (r.encryption_mode=$2 OR ($5 AND r.encryption_mode=3))) AND `+r.ReadSQL("chat_members.chat_room_id", "$1")+` ORDER BY owned_updated_at DESC,id LIMIT $3 OFFSET $4`, actor, r.policy.NoneEncryptionMode, take, offset, r.mls)
	if e != nil {
		return nil, e
	}
	out := []rooms.Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		v, e := r.room(ctx, tx, out[i].ChatRoomID, false)
		if e != nil {
			return nil, e
		}
		out[i].Room = &v
	}
	return out, tx.Commit()
}

func (r *Repository) ChangeMember(ctx context.Context, actor, rid string, action rooms.MemberAction, target string) (rooms.Member, error) {
	if e := valid(actor, rid); e != nil {
		return rooms.Member{}, e
	}
	if action == rooms.Invite || action == rooms.Remove {
		if e := valid(target); e != nil {
			return rooms.Member{}, e
		}
	} else {
		target = actor
	}
	permission, e := memberPermission(action)
	if e != nil {
		return rooms.Member{}, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, permission, "", target)
	if e != nil {
		return rooms.Member{}, e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, true)
	if e != nil {
		return rooms.Member{}, e
	}
	if e = account(ctx, tx, actor); e != nil {
		return rooms.Member{}, e
	}
	if v.Type == 1 && action != rooms.Leave {
		return rooms.Member{}, rooms.ErrUnsupported
	}
	if v.RealmID != nil && action != rooms.Leave && action != rooms.Decline {
		if _, e = r.social.RealmRoleForWriteInTransaction(ctx, tx, actor, *v.RealmID); e != nil {
			return rooms.Member{}, rooms.ErrForbidden
		}
		if action == rooms.Invite {
			if _, e = r.social.RealmRoleInTransaction(ctx, tx, target, *v.RealmID); e != nil {
				return rooms.Member{}, rooms.ErrForbidden
			}
		}
	}
	if action == rooms.Invite || action == rooms.Remove {
		if target == actor {
			return rooms.Member{}, rooms.ErrForbidden
		}
		if e = r.manage(ctx, tx, v, actor); e != nil {
			return rooms.Member{}, e
		}
		if r.social != nil && action == rooms.Invite {
			blocked, x := r.social.IsBlockedInTransaction(ctx, tx, actor, target)
			if x != nil {
				return rooms.Member{}, x
			}
			if blocked {
				return rooms.Member{}, rooms.ErrForbidden
			}
		}
		if v.RealmID != nil && action == rooms.Remove {
			a, x := r.social.RealmRoleInTransaction(ctx, tx, actor, *v.RealmID)
			b, y := r.social.RealmRoleInTransaction(ctx, tx, target, *v.RealmID)
			if x != nil {
				return rooms.Member{}, rooms.ErrForbidden
			}
			if y == nil && b >= a {
				return rooms.Member{}, rooms.ErrForbidden
			}
			if y != nil && !errors.Is(y, social.ErrForbidden) && !errors.Is(y, social.ErrNotFound) {
				return rooms.Member{}, y
			}
		}
	}
	m, e := member(ctx, tx, rid, target, false)
	exists := e == nil
	if e != nil && !errors.Is(e, rooms.ErrNotFound) {
		return m, e
	}
	var banned bool
	if exists {
		if e = tx.QueryRowContext(ctx, `SELECT owned_banned FROM chat_members WHERE id=$1`, m.ID).Scan(&banned); e != nil {
			return m, e
		}
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return m, e
	}
	switch action {
	case rooms.Invite:
		if e = account(ctx, tx, target); e != nil {
			return m, e
		}
		if exists && m.JoinedAt != nil && m.LeaveAt == nil && !banned {
			return m, rooms.ErrConflict
		}
		if !exists {
			mid, e := id()
			if e != nil {
				return m, e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,owned_updated_at) VALUES($1,$2,$3,$4,$5)`, mid, rid, target, r.policy.NotifyAll, now)
			if e != nil {
				return m, e
			}
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE chat_members SET joined_at=NULL,leave_at=NULL,last_read_at=NULL,owned_banned=false,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
			if e != nil {
				return m, e
			}
		}
	case rooms.Join:
		if !v.IsPublic || !v.IsCommunity || banned {
			return m, rooms.ErrForbidden
		}
		if exists && m.JoinedAt != nil && m.LeaveAt == nil {
			return m, r.commit(ctx, tx, actor, permission)
		}
		if !exists {
			mid, e := id()
			if e != nil {
				return m, e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at,last_read_at,owned_updated_at) VALUES($1,$2,$3,$4,$5,$5,$5)`, mid, rid, actor, r.policy.NotifyAll, now)
			if e != nil {
				return m, e
			}
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE chat_members SET joined_at=$2,leave_at=NULL,last_read_at=$2,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
			if e != nil {
				return m, e
			}
		}
	case rooms.Accept, rooms.Decline:
		if !exists || m.JoinedAt != nil || m.LeaveAt != nil || banned {
			return m, rooms.ErrNotFound
		}
		if action == rooms.Accept {
			_, e = tx.ExecContext(ctx, `UPDATE chat_members SET joined_at=$2,last_read_at=$2,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE chat_members SET leave_at=$2,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
		}
		if e != nil {
			return m, e
		}
	case rooms.Leave, rooms.Remove:
		if target == v.AccountID && v.RealmID == nil {
			return m, rooms.ErrForbidden
		}
		if !exists || m.LeaveAt != nil || action == rooms.Leave && m.JoinedAt == nil {
			return m, rooms.ErrNotFound
		}
		_, e = tx.ExecContext(ctx, `UPDATE chat_members SET leave_at=$2,owned_banned=$3,owned_updated_at=$2 WHERE id=$1`, m.ID, now, action == rooms.Remove)
		if e != nil {
			return m, e
		}
	default:
		return rooms.Member{}, rooms.ErrInvalid
	}
	m, e = member(ctx, tx, rid, target, false)
	if e != nil {
		return m, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE chat_rooms SET owned_updated_at=$2 WHERE id=$1`, rid, now); e != nil {
		return m, e
	}
	return m, r.commit(ctx, tx, actor, permission)
}
func (r *Repository) SetNotify(ctx context.Context, actor, rid string, n int32) (rooms.Member, error) {
	if e := valid(actor, rid); e != nil {
		return rooms.Member{}, e
	}
	if !r.policy.ValidNotify(n) {
		return rooms.Member{}, rooms.ErrInvalid
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "", "")
	if e != nil {
		return rooms.Member{}, e
	}
	defer tx.Rollback()
	if _, e = r.room(ctx, tx, rid, true); e != nil {
		return rooms.Member{}, e
	}
	allowed, x := r.CanReadInTransaction(ctx, tx, actor, rid)
	if x != nil {
		return rooms.Member{}, x
	}
	if !allowed {
		return rooms.Member{}, rooms.ErrForbidden
	}
	m, e := requireMember(ctx, tx, rid, actor)
	if e != nil {
		return m, e
	}
	m, e = scanMember(tx.QueryRowContext(ctx, `UPDATE chat_members SET notify=$2,owned_updated_at=clock_timestamp() WHERE id=$1 RETURNING `+memberColumns, m.ID, n))
	if e != nil {
		return m, e
	}
	return m, r.commit(ctx, tx, actor, "")
}

// MarkRead advances only to the timestamp of a visible text message actually in
// this room. Client wall clocks cannot suppress future unread messages.
func (r *Repository) MarkRead(ctx context.Context, actor, rid, messageID string) (rooms.Member, error) {
	if e := valid(actor, rid, messageID); e != nil {
		return rooms.Member{}, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "", "")
	if e != nil {
		return rooms.Member{}, e
	}
	defer tx.Rollback()
	if _, e = r.room(ctx, tx, rid, true); e != nil {
		return rooms.Member{}, e
	}
	allowed, x := r.CanReadInTransaction(ctx, tx, actor, rid)
	if x != nil {
		return rooms.Member{}, x
	}
	if !allowed {
		return rooms.Member{}, rooms.ErrForbidden
	}
	m, e := requireMember(ctx, tx, rid, actor)
	if e != nil {
		return m, e
	}
	var at sql.NullTime
	e = tx.QueryRowContext(ctx, `SELECT created_at FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL AND type='text'`, messageID, rid).Scan(&at)
	if errors.Is(e, sql.ErrNoRows) {
		return m, rooms.ErrNotFound
	}
	if e != nil {
		return m, e
	}
	m, e = scanMember(tx.QueryRowContext(ctx, `UPDATE chat_members SET last_read_at=greatest(last_read_at,$2::timestamptz),owned_updated_at=clock_timestamp() WHERE id=$1 RETURNING `+memberColumns, m.ID, at.Time))
	if e != nil {
		return m, e
	}
	return m, r.commit(ctx, tx, actor, "")
}

// Permissions match the owned HTTP routes; accepting, declining, joining and
// leaving affect only the verified actor and require authentication only.
func memberPermission(action rooms.MemberAction) (string, error) {
	switch action {
	case rooms.Invite:
		return "chat.invites.manage", nil
	case rooms.Remove:
		return "chat.members.kick", nil
	case rooms.Accept, rooms.Decline, rooms.Join, rooms.Leave:
		return "", nil
	default:
		return "", rooms.ErrInvalid
	}
}
