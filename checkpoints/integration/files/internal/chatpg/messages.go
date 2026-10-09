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

const textColumns = `id::text,chat_room_id::text,sender_id::text,type,room_sequence,content,client_message_id,created_at,updated_at,deleted_at,edited_at,meta,replied_message_id::text,reactions_count,thread_id::text,forwarded_message_id::text,encryption_meta`

func textMessage(s scanner) (messager.MessageProjection, error) {
	v := messager.MessageProjection{Meta: json.RawMessage(`null`), MembersMentioned: json.RawMessage(`[]`), Attachments: json.RawMessage(`[]`), EncryptionMeta: json.RawMessage(`null`), ReactionsCount: json.RawMessage(`{}`), ReactionsMade: map[string]bool{}}
	var meta, reactions, encryption []byte
	e := s.Scan(&v.ID, &v.ChatRoomID, &v.SenderID, &v.Type, &v.RoomSequence, &v.Content, &v.ClientMessageID, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt, &v.EditedAt, &meta, &v.RepliedMessageID, &reactions, &v.ThreadID, &v.ForwardedMessageID, &encryption)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	if len(meta) > 0 {
		v.Meta = meta
	}
	if len(reactions) > 0 {
		v.ReactionsCount = reactions
	}
	if len(encryption) > 0 {
		v.EncryptionMeta = encryption
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
	return textMessage(tx.QueryRowContext(ctx, `INSERT INTO chat_messages(id,chat_room_id,sender_id,type,room_sequence,content,client_message_id,created_at,updated_at,edited_at,meta,members_mentioned,replied_message_id,owned_request_hash,owned_creation_client_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,$10::jsonb,'[]',$11,$12,$7) RETURNING `+textColumns, mid, rid, sender, kind, seq, content, client, at, edited, meta, reply, hash))
}
func (r *Repository) Send(ctx context.Context, actor, rid string, p rooms.TextRequest) (messager.MessageProjection, error) {
	if e := valid(actor, rid); e != nil {
		return messager.MessageProjection{}, e
	}
	p, e := rooms.ValidateText(p, false)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	tx, e := r.beginMessageWrite(ctx, actor, rid, p.ForwardedMessageID)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	defer tx.Rollback()
	m, e := r.writable(ctx, tx, actor, rid)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	if e = r.mlsMessageCheck(ctx, tx, actor, rid, p.EncryptionMeta, false); e != nil {
		return messager.MessageProjection{}, e
	}
	if p.ThreadID != nil || p.ForwardedMessageID != nil || p.IsThreadRoot {
		room, e := r.room(ctx, tx, rid, false)
		if e != nil {
			return messager.MessageProjection{}, e
		}
		if room.EncryptionMode != r.policy.NoneEncryptionMode {
			return messager.MessageProjection{}, rooms.ErrUnsupported
		}
	}
	if e = r.validateThread(ctx, tx, rid, p); e != nil {
		return messager.MessageProjection{}, e
	}
	if p.ForwardedMessageID != nil {
		if _, e = r.sourceMessage(ctx, tx, actor, *p.ForwardedMessageID, false); e != nil {
			return messager.MessageProjection{}, e
		}
	}
	p.EncryptionMeta = normalizedMLSEncryption(ctx, p.EncryptionMeta)
	meta := mergeMeta(nil, p)
	encoded, _ := json.Marshal(struct {
		Content     string
		Reply       *string
		Attachments *[]string
		Meta        json.RawMessage
	}{p.Content, p.RepliedMessageID, p.AttachmentsID, meta})
	// Preserve pre-rich-message idempotency digests for existing plain-text rows.
	if p.AttachmentsID == nil && len(p.Meta) == 0 && p.Embeds == nil {
		encoded, _ = json.Marshal(struct {
			Content string
			Reply   *string
		}{p.Content, p.RepliedMessageID})
	}
	if p.EncryptionMeta != nil {
		encoded, _ = json.Marshal(struct {
			Plain      json.RawMessage
			Encryption *rooms.EncryptionMeta
		}{encoded, p.EncryptionMeta})
	}
	if p.ThreadID != nil || p.ForwardedMessageID != nil || p.IsThreadRoot {
		encoded, _ = json.Marshal(struct {
			Plain           json.RawMessage
			Thread, Forward *string
			Root            bool
		}{encoded, p.ThreadID, p.ForwardedMessageID, p.IsThreadRoot})
	}
	digest := sha256.Sum256(encoded)
	if p.ClientMessageID != nil {
		var stored []byte
		var prior string
		e = tx.QueryRowContext(ctx, `SELECT id::text,owned_request_hash FROM chat_messages WHERE chat_room_id=$1 AND sender_id=$2 AND type='text' AND coalesce(owned_creation_client_id,client_message_id)=$3 AND owned_request_hash IS NOT NULL`, rid, m.ID, *p.ClientMessageID).Scan(&prior, &stored)
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
			if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
				return v, e
			}
			if p.ForwardedMessageID != nil {
				if _, e = r.sourceMessage(ctx, tx, actor, *p.ForwardedMessageID, false); e != nil {
					return messager.MessageProjection{}, e
				}
			}
			if _, e = r.writable(ctx, tx, actor, rid); e != nil {
				return messager.MessageProjection{}, e
			}
			return v, r.commit(ctx, tx, actor, "chat.messages.create")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return messager.MessageProjection{}, e
		}
		var occupied bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages WHERE chat_room_id=$1 AND sender_id=$2 AND type='text' AND client_message_id=$3 AND owned_request_hash IS NOT NULL)`, rid, m.ID, *p.ClientMessageID).Scan(&occupied); e != nil {
			return messager.MessageProjection{}, e
		}
		if occupied {
			return messager.MessageProjection{}, rooms.ErrConflict
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
	var content *string
	if p.Content != "" {
		content = &p.Content
	}
	v, e := appendMessage(ctx, tx, rid, m.ID, "text", content, p.ClientMessageID, p.RepliedMessageID, meta, digest[:], nil)
	if e != nil {
		return v, e
	}
	if p.ThreadID != nil || p.ForwardedMessageID != nil {
		v, e = textMessage(tx.QueryRowContext(ctx, `UPDATE chat_messages SET thread_id=$2,forwarded_message_id=$3 WHERE id=$1 RETURNING `+textColumns, v.ID, p.ThreadID, p.ForwardedMessageID))
		if e != nil {
			return v, e
		}
	}
	if e = setMessageEncryption(ctx, tx, &v, p.EncryptionMeta); e != nil {
		return v, e
	}
	if e = r.applyAttachments(ctx, tx, actor, &v, p.AttachmentsID); e != nil {
		return v, e
	}
	if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	if messageEmpty(v) {
		return messager.MessageProjection{}, rooms.ErrInvalid
	}
	if p.ForwardedMessageID != nil {
		if _, e = r.sourceMessage(ctx, tx, actor, *p.ForwardedMessageID, false); e != nil {
			return messager.MessageProjection{}, e
		}
	}
	if _, e = r.writable(ctx, tx, actor, rid); e != nil {
		return messager.MessageProjection{}, e
	}
	if r.textObserver != nil && !botWriteFrom(ctx) {
		if e = r.textObserver.QueueMessage(ctx, tx, actor, rid, v.ID); e != nil {
			return messager.MessageProjection{}, e
		}
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
	if e = r.mlsMessageCheck(ctx, tx, actor, rid, p.EncryptionMeta, false); e != nil {
		return messager.MessageProjection{}, e
	}
	p.EncryptionMeta = normalizedMLSEncryption(ctx, p.EncryptionMeta)
	v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, mid, rid))
	if e != nil {
		return v, e
	}
	if v.SenderID != m.ID {
		return messager.MessageProjection{}, rooms.ErrForbidden
	}
	if p.ThreadID != nil && !sameOptional(p.ThreadID, v.ThreadID) || p.ForwardedMessageID != nil && !sameOptional(p.ForwardedMessageID, v.ForwardedMessageID) || p.IsThreadRoot && (v.ThreadID != nil || v.RepliedMessageID != nil) {
		return messager.MessageProjection{}, rooms.ErrConflict
	}
	if v.Type != "text" {
		return messager.MessageProjection{}, rooms.ErrUnsupported
	}
	if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	previousAttachments := append([]byte(nil), v.Attachments...)
	content := v.Content
	if p.EncryptionMeta != nil {
		content = nil
	}
	if p.ContentSet || p.Content != "" {
		content = &p.Content
	}
	// The authored service ignores arbitrary meta and reply changes on edits.
	metaPatch := p
	metaPatch.Meta = nil
	meta := mergeMeta(v.Meta, metaPatch)
	if p.EncryptionMeta != nil {
		meta = mergeMeta(nil, p)
	}
	client := v.ClientMessageID
	if p.ClientMessageID != nil {
		client = p.ClientMessageID
	}
	sameClient := (client == nil && v.ClientMessageID == nil) || (client != nil && v.ClientMessageID != nil && *client == *v.ClientMessageID)
	if !sameClient {
		var occupied bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages WHERE chat_room_id=$1 AND sender_id=$2 AND id<>$3 AND type='text' AND owned_request_hash IS NOT NULL AND(client_message_id=$4 OR coalesce(owned_creation_client_id,client_message_id)=$4))`, rid, m.ID, mid, *client).Scan(&occupied); e != nil {
			return v, e
		}
		if occupied {
			return v, rooms.ErrConflict
		}
	}
	encryptionUnchanged := true
	if p.EncryptionMeta != nil {
		raw, _ := json.Marshal(p.EncryptionMeta)
		var prior rooms.EncryptionMeta
		priorErr := json.Unmarshal(v.EncryptionMeta, &prior)
		canonicalPrior, _ := json.Marshal(prior)
		encryptionUnchanged = priorErr == nil && bytes.Equal(canonicalPrior, raw)
		v.EncryptionMeta = raw
	}
	unchanged := encryptionUnchanged && ((content == nil && v.Content == nil) || (content != nil && v.Content != nil && *content == *v.Content)) && bytes.Equal(meta, mergeMeta(v.Meta, rooms.TextRequest{}))
	v.Content = content
	v.Meta = meta
	if e = r.applyAttachments(ctx, tx, actor, &v, p.AttachmentsID); e != nil {
		return v, e
	}
	if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	if p.EncryptionMeta != nil {
		v.EncryptionMeta, _ = json.Marshal(p.EncryptionMeta)
	}
	if messageEmpty(v) {
		return messager.MessageProjection{}, rooms.ErrInvalid
	}
	if unchanged && sameClient && bytes.Equal(previousAttachments, v.Attachments) {
		return v, r.commit(ctx, tx, actor, "chat.messages.update")
	}
	eventMeta := map[string]any{}
	_ = json.Unmarshal(meta, &eventMeta)
	if eventMeta == nil {
		eventMeta = map[string]any{}
	}
	eventMeta["message_id"] = v.ID
	encoded, _ := json.Marshal(eventMeta)
	event, e := appendMessage(ctx, tx, rid, m.ID, "messages.update", content, nil, v.RepliedMessageID, encoded, nil, nil)
	if e != nil {
		return v, e
	}
	if e = setMessageEncryption(ctx, tx, &event, p.EncryptionMeta); e != nil {
		return v, e
	}
	if e = setMessageEncryption(ctx, tx, &v, p.EncryptionMeta); e != nil {
		return v, e
	}
	v, e = textMessage(tx.QueryRowContext(ctx, `UPDATE chat_messages SET content=$2,meta=$3::jsonb,edited_at=$4,updated_at=$4,owned_creation_client_id=coalesce(owned_creation_client_id,client_message_id),client_message_id=$5 WHERE id=$1 RETURNING `+textColumns, mid, content, meta, event.CreatedAt, client))
	if e != nil {
		return v, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE chat_messages SET edited_at=$2 WHERE id=$1`, event.ID, event.CreatedAt); e != nil {
		return v, e
	}
	if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
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
	deletion := rooms.MLSWriteFrom(ctx)
	if e = r.mlsMessageCheck(ctx, tx, actor, rid, deletion.Encryption, true); e != nil {
		return e
	}
	v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2`, mid, rid))
	if e != nil {
		return e
	}
	if v.SenderID != m.ID {
		return rooms.ErrForbidden
	}
	if v.Type != "text" && v.Type != "voice" && v.Type != "placeholder" {
		return rooms.ErrUnsupported
	}
	if v.DeletedAt != nil {
		return r.commit(ctx, tx, actor, "chat.messages.delete")
	}
	meta, _ := json.Marshal(map[string]string{"message_id": v.ID})
	event, e := appendMessage(ctx, tx, rid, m.ID, "messages.delete", nil, deletion.ClientMessageID, nil, meta, nil, nil)
	if e != nil {
		return e
	}
	if e = setMessageEncryption(ctx, tx, &event, normalizedMLSEncryption(ctx, deletion.Encryption)); e != nil {
		return e
	}
	// Hide previous edit snapshots along with the original text. The tombstone
	// remains sync-visible without retaining plaintext in its event metadata.
	_, e = tx.ExecContext(ctx, `UPDATE chat_messages SET deleted_at=$3,updated_at=$3 WHERE chat_room_id=$1 AND (id=$2::uuid OR type IN('messages.update','messages.sync.finalize','messages.placeholder.update') AND meta->>'message_id'=$2::text) AND deleted_at IS NULL`, rid, mid, event.CreatedAt)
	if e != nil {
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
	return r.commit(ctx, tx, actor, "chat.messages.delete")
}
