// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

func (r *Repository) requireModerator(ctx context.Context, tx *sql.Tx, actor, rid string) (rooms.Room, rooms.Member, error) {
	v, e := r.detailRoom(ctx, tx, actor, rid, false)
	if e != nil {
		return v, rooms.Member{}, e
	}
	if v.Type != 0 || v.RealmID == nil {
		return v, rooms.Member{}, rooms.ErrForbidden
	}
	if e = r.realmPermission(ctx, tx, actor, *v.RealmID, social.CanModerateChat); e != nil {
		return v, rooms.Member{}, e
	}
	m, e := r.writable(ctx, tx, actor, rid)
	return v, m, e
}
func (r *Repository) ModerateDelete(ctx context.Context, actor, rid, mid string, q rooms.ModerationDelete) error {
	if e := valid(actor, rid, mid); e != nil {
		return e
	}
	if e := rooms.ValidateModerationReason(q.Reason); e != nil {
		return e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.delete", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	room, mod, e := r.requireModerator(ctx, tx, actor, rid)
	if e != nil {
		return e
	}
	// No invented plaintext moderation event in an encrypted group.
	if room.EncryptionMode != r.policy.NoneEncryptionMode {
		return rooms.ErrUnsupported
	}
	v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2`, mid, rid))
	if e != nil {
		return e
	}
	if v.Type != "text" {
		return rooms.ErrUnsupported
	}
	if v.DeletedAt != nil {
		return r.commit(ctx, tx, actor, "chat.messages.delete")
	}
	meta, _ := json.Marshal(map[string]string{"message_id": mid})
	event, e := appendMessage(ctx, tx, rid, mod.ID, "messages.delete", nil, nil, nil, meta, nil, nil)
	if e != nil {
		return e
	}
	// Hide all prior snapshots and release canonical associations atomically, as
	// the existing self-delete does. Neither the event nor audit retains content.
	if _, e = tx.ExecContext(ctx, `UPDATE chat_messages SET deleted_at=$3,updated_at=$3,content=CASE WHEN id=$2::uuid THEN '[Message deleted by moderator]' ELSE NULL END WHERE chat_room_id=$1 AND(id=$2::uuid OR type='messages.update' AND meta->>'message_id'=$2::text) AND deleted_at IS NULL`, rid, mid, event.CreatedAt); e != nil {
		return e
	}
	if r.associations != nil {
		if e = r.associations.Remove(ctx, tx, mid); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE chat_reactions SET deleted_at=$2,owned_updated_at=$2 WHERE message_id=$1 AND deleted_at IS NULL`, mid, event.CreatedAt); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_detail_audit(actor_id,room_id,member_id,message_id,action,reason,occurred_at)VALUES($1,$2,$3,$4,'message.moderated',$5,$6)`, actor, rid, mod.ID, mid, q.Reason, event.CreatedAt); e != nil {
		return e
	}
	if _, _, e = r.requireModerator(ctx, tx, actor, rid); e != nil {
		return e
	}
	return r.commit(ctx, tx, actor, "chat.messages.delete")
}
func (r *Repository) ModerateTimeout(ctx context.Context, actor, rid, target string, q rooms.ModerationTimeout) (out rooms.ModerationTimeoutResult, e error) {
	if e = valid(actor, rid, target); e != nil {
		return
	}
	if actor == target || q.DurationMinutes < 1 || q.DurationMinutes > 30*24*60 {
		return out, rooms.ErrInvalid
	}
	if e = rooms.ValidateModerationReason(q.Reason); e != nil {
		return
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.members.timeout", "", target)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	check := func() (rooms.Member, rooms.Member, error) {
		room, mod, x := r.requireModerator(ctx, tx, actor, rid)
		if x != nil {
			return mod, rooms.Member{}, x
		}
		m, x := member(ctx, tx, rid, target, true)
		if x != nil {
			return mod, m, x
		}
		a, x := r.social.RealmRoleInTransaction(ctx, tx, actor, *room.RealmID)
		if x != nil {
			return mod, m, x
		}
		b, x := r.social.RealmRoleInTransaction(ctx, tx, target, *room.RealmID)
		if x != nil {
			return mod, m, x
		}
		if b >= a {
			return mod, m, rooms.ErrForbidden
		}
		if x = activeOther(ctx, tx, target); x != nil {
			return mod, m, x
		}
		return mod, m, nil
	}
	mod, m, e := check()
	if e != nil {
		return out, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	until := now.Add(time.Duration(q.DurationMinutes) * time.Minute)
	if _, e = tx.ExecContext(ctx, `UPDATE chat_members SET owned_timeout_until=$2,owned_timeout_reason=$3,owned_timeout_sender=$4,owned_timeout_since=$5,owned_updated_at=$5 WHERE id=$1`, m.ID, until, q.Reason, mod.ID, now); e != nil {
		return
	}
	if e = relationAudit(ctx, tx, actor, rid, "member.timeout", target, now); e != nil {
		return
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_detail_audit(actor_id,room_id,member_id,action,reason,occurred_at)VALUES($1,$2,$3,'member.timeout',$4,$5)`, actor, rid, m.ID, q.Reason, now); e != nil {
		return
	}
	if _, _, e = check(); e != nil {
		return
	}
	if e = r.commit(ctx, tx, actor, "chat.members.timeout"); e != nil {
		return
	}
	return rooms.ModerationTimeoutResult{Success: true, Message: "User timed out", TimeoutUntil: until}, nil
}
