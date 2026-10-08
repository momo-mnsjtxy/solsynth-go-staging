// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"time"
)

const textColumns = `id::text,chat_room_id::text,sender_id::text,type,room_sequence,content,client_message_id,created_at,updated_at,deleted_at,edited_at,meta,replied_message_id::text`

func textMessage(s scanner) (messager.MessageProjection, error) {
	v := messager.MessageProjection{Meta: json.RawMessage(`null`), MembersMentioned: json.RawMessage(`[]`), Attachments: json.RawMessage(`[]`), EncryptionMeta: json.RawMessage(`null`), ReactionsCount: json.RawMessage(`{}`), ReactionsMade: map[string]bool{}}
	var meta []byte
	e := s.Scan(&v.ID, &v.ChatRoomID, &v.SenderID, &v.Type, &v.RoomSequence, &v.Content, &v.ClientMessageID, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt, &v.EditedAt, &meta, &v.RepliedMessageID)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	if len(meta) > 0 {
		v.Meta = meta
	}
	return v, e
}
func (r *Repository) writable(ctx context.Context, tx *sql.Tx, actor, rid string) (rooms.Member, error) {
	if _, e := r.room(ctx, tx, rid, true); e != nil {
		return rooms.Member{}, e
	}
	if e := account(ctx, tx, actor); e != nil {
		return rooms.Member{}, e
	}
	m, e := requireMember(ctx, tx, rid, actor)
	if e != nil {
		return m, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return m, e
	}
	if m.TimeoutUntil != nil && m.TimeoutUntil.After(now) {
		return m, rooms.ErrForbidden
	}
	if r.social != nil {
		var allowed bool
		if e = tx.QueryRowContext(ctx, `SELECT `+r.SendSQL("$1", "$2"), rid, actor).Scan(&allowed); e != nil {
			return m, e
		}
		if !allowed {
			return m, rooms.ErrForbidden
		}
	}
	return m, nil
}

// All owned room writes take its row lock first. Sequence allocation, original
// row mutations and sync events commit in the same transaction across replicas.
func allocate(ctx context.Context, tx *sql.Tx, rid string) (int64, time.Time, error) {
	var n int64
	var at time.Time
	e := tx.QueryRowContext(ctx, `UPDATE chat_rooms SET owned_next_sequence=greatest(owned_next_sequence,(SELECT coalesce(max(room_sequence),0) FROM chat_messages WHERE chat_room_id=$1))+1,owned_updated_at=greatest(clock_timestamp(),coalesce((SELECT max(created_at)+interval '1 microsecond' FROM chat_messages WHERE chat_room_id=$1),clock_timestamp())) WHERE id=$1 RETURNING owned_next_sequence,owned_updated_at`, rid).Scan(&n, &at)
	return n, at, e
}
func appendMessage(ctx context.Context, tx *sql.Tx, rid, sender, kind string, content, client, reply *string, meta []byte, hash []byte, edited *time.Time) (messager.MessageProjection, error) {
	mid, e := id()
	if e != nil {
		return messager.MessageProjection{}, e
	}
	seq, at, e := allocate(ctx, tx, rid)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	if len(meta) == 0 {
		meta = []byte(`null`)
	}
	return textMessage(tx.QueryRowContext(ctx, `INSERT INTO chat_messages(id,chat_room_id,sender_id,type,room_sequence,content,client_message_id,created_at,updated_at,edited_at,meta,members_mentioned,replied_message_id,owned_request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,$10::jsonb,'[]',$11,$12) RETURNING `+textColumns, mid, rid, sender, kind, seq, content, client, at, edited, meta, reply, hash))
}
func (r *Repository) Send(ctx context.Context, actor, rid string, p rooms.TextRequest) (messager.MessageProjection, error) {
	if e := valid(actor, rid); e != nil {
		return messager.MessageProjection{}, e
	}
	p, e := rooms.ValidateText(p, false)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.create", "")
	if e != nil {
		return messager.MessageProjection{}, e
	}
	defer tx.Rollback()
	m, e := r.writable(ctx, tx, actor, rid)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	encoded, _ := json.Marshal(struct {
		Content string
		Reply   *string
	}{p.Content, p.RepliedMessageID})
	digest := sha256.Sum256(encoded)
	if p.ClientMessageID != nil {
		var stored []byte
		var prior string
		e = tx.QueryRowContext(ctx, `SELECT id::text,owned_request_hash FROM chat_messages WHERE chat_room_id=$1 AND sender_id=$2 AND type='text' AND client_message_id=$3 AND owned_request_hash IS NOT NULL`, rid, m.ID, *p.ClientMessageID).Scan(&prior, &stored)
		if e == nil {
			if !bytes.Equal(stored, digest[:]) {
				return messager.MessageProjection{}, rooms.ErrConflict
			}
			v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1`, prior))
			if e != nil {
				return v, e
			}
			if v.DeletedAt != nil {
				return messager.MessageProjection{}, rooms.ErrConflict
			}
			return v, r.commit(ctx, tx, actor, "chat.messages.create")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return messager.MessageProjection{}, e
		}
	}
	if p.RepliedMessageID != nil {
		var ok bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND type='text' AND deleted_at IS NULL)`, *p.RepliedMessageID, rid).Scan(&ok)
		if e != nil {
			return messager.MessageProjection{}, e
		}
		if !ok {
			return messager.MessageProjection{}, rooms.ErrNotFound
		}
	}
	v, e := appendMessage(ctx, tx, rid, m.ID, "text", &p.Content, p.ClientMessageID, p.RepliedMessageID, nil, digest[:], nil)
	if e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "chat.messages.create")
}
func (r *Repository) Edit(ctx context.Context, actor, rid, mid string, p rooms.TextRequest) (messager.MessageProjection, error) {
	if e := valid(actor, rid, mid); e != nil {
		return messager.MessageProjection{}, e
	}
	p, e := rooms.ValidateText(p, true)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.update", "")
	if e != nil {
		return messager.MessageProjection{}, e
	}
	defer tx.Rollback()
	m, e := r.writable(ctx, tx, actor, rid)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, mid, rid))
	if e != nil {
		return v, e
	}
	if v.SenderID != m.ID {
		return messager.MessageProjection{}, rooms.ErrForbidden
	}
	if v.Type != "text" {
		return messager.MessageProjection{}, rooms.ErrUnsupported
	}
	if v.Content != nil && *v.Content == p.Content {
		return v, r.commit(ctx, tx, actor, "chat.messages.update")
	}
	meta, _ := json.Marshal(map[string]string{"message_id": v.ID})
	event, e := appendMessage(ctx, tx, rid, m.ID, "messages.update", &p.Content, nil, v.RepliedMessageID, meta, nil, nil)
	if e != nil {
		return v, e
	}
	v, e = textMessage(tx.QueryRowContext(ctx, `UPDATE chat_messages SET content=$2,edited_at=$3,updated_at=$3 WHERE id=$1 RETURNING `+textColumns, mid, p.Content, event.CreatedAt))
	if e != nil {
		return v, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE chat_messages SET edited_at=$2 WHERE id=$1`, event.ID, event.CreatedAt); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "chat.messages.update")
}
func (r *Repository) DeleteMessage(ctx context.Context, actor, rid, mid string) error {
	if e := valid(actor, rid, mid); e != nil {
		return e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.delete", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	m, e := r.writable(ctx, tx, actor, rid)
	if e != nil {
		return e
	}
	v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2`, mid, rid))
	if e != nil {
		return e
	}
	if v.SenderID != m.ID {
		return rooms.ErrForbidden
	}
	if v.Type != "text" {
		return rooms.ErrUnsupported
	}
	if v.DeletedAt != nil {
		return r.commit(ctx, tx, actor, "chat.messages.delete")
	}
	meta, _ := json.Marshal(map[string]string{"message_id": v.ID})
	event, e := appendMessage(ctx, tx, rid, m.ID, "messages.delete", nil, nil, nil, meta, nil, nil)
	if e != nil {
		return e
	}
	// Hide previous edit snapshots along with the original text. The tombstone
	// remains sync-visible without retaining plaintext in its event metadata.
	_, e = tx.ExecContext(ctx, `UPDATE chat_messages SET deleted_at=$3,updated_at=$3 WHERE chat_room_id=$1 AND (id=$2::uuid OR type='messages.update' AND meta->>'message_id'=$2::text) AND deleted_at IS NULL`, rid, mid, event.CreatedAt)
	if e != nil {
		return e
	}
	return r.commit(ctx, tx, actor, "chat.messages.delete")
}
