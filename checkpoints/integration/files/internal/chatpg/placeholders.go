// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"

	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

//go:embed transient_schema.sql
var TransientSchema string

func (r *Repository) TransientReady(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT message_id,request_hash,finalized_at FROM owned_chat_placeholder_finalizations LIMIT 0`)
	return e
}
func placeholderState(v messager.MessageProjection) (messager.PlaceholderSnapshot, error) {
	var meta map[string]json.RawMessage
	if json.Unmarshal(v.Meta, &meta) != nil || meta == nil || v.Type != "placeholder" {
		return messager.PlaceholderSnapshot{}, rooms.ErrConflict
	}
	var kind string
	if json.Unmarshal(meta["placeholder_kind"], &kind) != nil || rooms.ValidatePlaceholderKind(kind) != nil {
		return messager.PlaceholderSnapshot{}, rooms.ErrConflict
	}
	state := &messager.ResolvedPlaceholder{Kind: messager.PlaceholderStreaming}
	if kind == "uploading" {
		state.Kind = messager.PlaceholderUploading
	}
	for k, target := range map[string]any{"placeholder_content": &state.Content, "placeholder_progress": &state.Progress, "placeholder_expires_at": &state.ExpiresAtMS} {
		if b, ok := meta[k]; ok {
			if json.Unmarshal(b, target) != nil {
				return messager.PlaceholderSnapshot{}, rooms.ErrConflict
			}
		}
	}
	for _, k := range []string{"placeholder_kind", "placeholder_content", "placeholder_progress", "placeholder_expires_at"} {
		delete(meta, k)
	}
	return messager.PlaceholderSnapshot{RoomID: v.ChatRoomID, SenderID: v.SenderID, Type: v.Type, Content: v.Content, Placeholder: state, OtherMeta: meta, DeletedAt: v.DeletedAt, UpdatedAt: v.UpdatedAt}, nil
}
func placeholderMeta(s messager.PlaceholderSnapshot) json.RawMessage {
	m := map[string]any{}
	for k, v := range s.OtherMeta {
		m[k] = v
	}
	if s.Placeholder != nil {
		p := s.Placeholder
		kind := "streaming"
		if p.Kind == messager.PlaceholderUploading {
			kind = "uploading"
		}
		m["placeholder_kind"] = kind
		m["placeholder_expires_at"] = p.ExpiresAtMS
		if p.Content != nil {
			m["placeholder_content"] = *p.Content
		}
		if p.Progress != nil {
			m["placeholder_progress"] = *p.Progress
		}
	}
	if len(m) == 0 {
		return json.RawMessage(`null`)
	}
	b, _ := json.Marshal(m)
	return b
}
func (r *Repository) plaintextWritable(ctx context.Context, tx *sql.Tx, actor, rid string) (rooms.Member, error) {
	m, e := r.writable(ctx, tx, actor, rid)
	if e != nil {
		return m, e
	}
	v, e := r.room(ctx, tx, rid, false)
	if e == nil && v.EncryptionMode != r.policy.NoneEncryptionMode {
		e = rooms.ErrUnsupported
	}
	return m, e
}
func (r *Repository) SendPlaceholder(ctx context.Context, actor, rid string, p rooms.PlaceholderRequest) (messager.MessageProjection, error) {
	if e := valid(actor, rid); e != nil {
		return messager.MessageProjection{}, e
	}
	if e := rooms.ValidatePlaceholderKind(p.Kind); e != nil {
		return messager.MessageProjection{}, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.create", "")
	if e != nil {
		return messager.MessageProjection{}, e
	}
	defer tx.Rollback()
	m, e := r.plaintextWritable(ctx, tx, actor, rid)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	prior, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE chat_room_id=$1 AND sender_id=$2 AND type='placeholder' AND deleted_at IS NULL`, rid, m.ID))
	if e != nil && !errors.Is(e, rooms.ErrNotFound) {
		return messager.MessageProjection{}, e
	}
	if e == nil {
		state, x := placeholderState(prior)
		if x != nil {
			return messager.MessageProjection{}, x
		}
		if state.Placeholder.Content != nil && strings.TrimSpace(*state.Placeholder.Content) != "" {
			if _, x = r.finalizePlaceholderTx(ctx, tx, actor, prior, rooms.PlaceholderFinalize{MessageID: prior.ID, Content: state.Placeholder.Content}, nil); x != nil {
				return messager.MessageProjection{}, x
			}
		} else {
			if x = r.expirePlaceholderTx(ctx, tx, prior); x != nil {
				return messager.MessageProjection{}, x
			}
		}
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	kind := messager.PlaceholderStreaming
	if p.Kind == "uploading" {
		kind = messager.PlaceholderUploading
	}
	transition, e := messager.SendPlaceholder(rid, m.ID, kind, nil, now)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	v, e := appendMessage(ctx, tx, rid, m.ID, "placeholder", nil, nil, nil, placeholderMeta(transition.Created), nil, nil)
	if e != nil {
		return v, e
	}
	if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	if _, e = r.plaintextWritable(ctx, tx, actor, rid); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "chat.messages.create")
}

// Resolve a packet's room only after nonlocking validation of its original
// connection proof. The mutation transaction repeats that preflight and locks
// canonical account, realm, then room before resolving the target again.
func (r *Repository) placeholderRoom(ctx context.Context, actor, mid string) (string, error) {
	if e := valid(actor, mid); e != nil {
		return "", e
	}
	if r == nil || (!r.fixture && r.guard == nil) {
		return "", rooms.ErrUnavailable
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if !r.fixture {
		if e = r.preflight(ctx, tx, actor, "chat.messages.update"); e != nil {
			return "", e
		}
	}
	var rid string
	e = tx.QueryRowContext(ctx, `SELECT chat_room_id::text FROM chat_messages WHERE id=$1`, mid).Scan(&rid)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	return rid, e
}
func (r *Repository) UpdatePlaceholder(ctx context.Context, actor string, p rooms.PlaceholderUpdate) (messager.MessageProjection, error) {
	if e := rooms.ValidatePlaceholderUpdate(p); e != nil {
		return messager.MessageProjection{}, e
	}
	rid, e := r.placeholderRoom(ctx, actor, p.MessageID)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.update", "")
	if e != nil {
		return messager.MessageProjection{}, e
	}
	defer tx.Rollback()
	m, e := r.plaintextWritable(ctx, tx, actor, rid)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, p.MessageID, rid))
	if e != nil {
		return v, e
	}
	if v.SenderID != m.ID {
		return v, rooms.ErrForbidden
	}
	s, e := placeholderState(v)
	if e != nil {
		return v, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return v, e
	}
	s, e = messager.UpdatePlaceholder(s, p.ContentChunk, p.Progress, now)
	if e != nil {
		return v, rooms.ErrConflict
	}
	// Bound accumulated chunks, not merely each individual packet.
	if s.Placeholder.Content != nil {
		if _, e = rooms.ValidateText(rooms.TextRequest{Content: *s.Placeholder.Content}, true); e != nil {
			return v, e
		}
	}
	v, e = textMessage(tx.QueryRowContext(ctx, `UPDATE chat_messages SET meta=$2::jsonb,updated_at=clock_timestamp() WHERE id=$1 RETURNING `+textColumns, v.ID, placeholderMeta(s)))
	if e != nil {
		return v, e
	}
	meta, _ := json.Marshal(map[string]string{"message_id": v.ID})
	if _, e = appendMessage(ctx, tx, rid, m.ID, "messages.placeholder.update", nil, nil, nil, meta, nil, nil); e != nil {
		return v, e
	}
	if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	if _, e = r.plaintextWritable(ctx, tx, actor, rid); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "chat.messages.update")
}
func (r *Repository) FinalizePlaceholder(ctx context.Context, actor string, p rooms.PlaceholderFinalize) (messager.MessageProjection, error) {
	if e := rooms.ValidatePlaceholderFinalize(p); e != nil {
		return messager.MessageProjection{}, e
	}
	rid, e := r.placeholderRoom(ctx, actor, p.MessageID)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "chat.messages.update", "")
	if e != nil {
		return messager.MessageProjection{}, e
	}
	defer tx.Rollback()
	m, e := r.plaintextWritable(ctx, tx, actor, rid)
	if e != nil {
		return messager.MessageProjection{}, e
	}
	v, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, p.MessageID, rid))
	if e != nil {
		return v, e
	}
	if v.SenderID != m.ID {
		return v, rooms.ErrForbidden
	}
	b, _ := json.Marshal(p)
	hash := sha256.Sum256(b)
	var prior []byte
	e = tx.QueryRowContext(ctx, `SELECT request_hash FROM owned_chat_placeholder_finalizations WHERE message_id=$1`, v.ID).Scan(&prior)
	if e == nil {
		if !bytes.Equal(prior, hash[:]) || v.Type != "text" {
			return v, rooms.ErrConflict
		}
		if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
			return v, e
		}
	} else if errors.Is(e, sql.ErrNoRows) {
		v, e = r.finalizePlaceholderTx(ctx, tx, actor, v, p, hash[:])
		if e != nil {
			return v, e
		}
	} else {
		return v, e
	}
	if _, e = r.plaintextWritable(ctx, tx, actor, rid); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "chat.messages.update")
}
func (r *Repository) finalizePlaceholderTx(ctx context.Context, tx *sql.Tx, actor string, v messager.MessageProjection, p rooms.PlaceholderFinalize, hash []byte) (messager.MessageProjection, error) {
	s, e := placeholderState(v)
	if e != nil {
		return v, e
	}
	s, e = messager.FinalizePlaceholder(s, p.Content, nil, nil)
	if e != nil {
		return v, rooms.ErrConflict
	}
	if _, e = rooms.ValidateText(rooms.TextRequest{Content: *s.Content, AttachmentsID: p.AttachmentsID}, true); e != nil {
		return v, e
	}
	v, e = textMessage(tx.QueryRowContext(ctx, `UPDATE chat_messages SET type='text',content=$2,meta=$3::jsonb,updated_at=clock_timestamp() WHERE id=$1 RETURNING `+textColumns, v.ID, s.Content, placeholderMeta(s)))
	if e != nil {
		return v, e
	}
	if e = r.applyAttachments(ctx, tx, actor, &v, p.AttachmentsID); e != nil {
		return v, e
	}
	if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	meta := map[string]any{}
	_ = json.Unmarshal(v.Meta, &meta)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["message_id"] = v.ID
	raw, _ := json.Marshal(meta)
	event, e := appendMessage(ctx, tx, v.ChatRoomID, v.SenderID, "messages.sync.finalize", v.Content, nil, nil, raw, nil, nil)
	if e != nil {
		return v, e
	}
	if hash != nil {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_placeholder_finalizations(message_id,request_hash,finalized_at)VALUES($1,$2,$3)`, v.ID, hash, event.CreatedAt); e != nil {
			return v, e
		}
	}
	return v, nil
}
func (r *Repository) expirePlaceholderTx(ctx context.Context, tx *sql.Tx, v messager.MessageProjection) error {
	meta, _ := json.Marshal(map[string]string{"message_id": v.ID})
	event, e := appendMessage(ctx, tx, v.ChatRoomID, v.SenderID, "messages.placeholder.expired", nil, nil, nil, meta, nil, nil)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE chat_messages SET deleted_at=$2,updated_at=$2 WHERE id=$1 AND type='placeholder' AND deleted_at IS NULL`, v.ID, event.CreatedAt)
	return e
}

// Bounded worker, no startup migration or in-memory timer. The room lock orders
// expiry against updates/finalization, and stale selection is repeated after it.
func (r *Repository) ExpirePlaceholders(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, rooms.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT DISTINCT chat_room_id::text FROM chat_messages WHERE type='placeholder' AND deleted_at IS NULL AND jsonb_typeof(meta->'placeholder_expires_at')='number' AND (meta->>'placeholder_expires_at')::numeric<=floor(extract(epoch FROM clock_timestamp())*1000) ORDER BY chat_room_id::text LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	var ids []string
	for rows.Next() {
		var rid string
		if e = rows.Scan(&rid); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, rid)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	for _, rid := range ids {
		n, e := r.expireRoomPlaceholders(ctx, rid, limit-count)
		count += n
		if e != nil {
			return count, e
		}
		if count == limit {
			break
		}
	}
	return count, nil
}
func (r *Repository) expireRoomPlaceholders(ctx context.Context, rid string, limit int) (int, error) {
	tx, e := r.begin(ctx, false)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM chat_rooms WHERE id=$1 FOR UPDATE`, rid); e != nil {
		return 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE chat_room_id=$1 AND type='placeholder' AND deleted_at IS NULL AND jsonb_typeof(meta->'placeholder_expires_at')='number' AND (meta->>'placeholder_expires_at')::numeric<=floor(extract(epoch FROM clock_timestamp())*1000) ORDER BY id LIMIT $2`, rid, limit)
	if e != nil {
		return 0, e
	}
	var list []messager.MessageProjection
	for rows.Next() {
		v, x := textMessage(rows)
		if x != nil {
			rows.Close()
			return 0, x
		}
		list = append(list, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return 0, e
	}
	n := 0
	for _, v := range list {
		s, x := placeholderState(v)
		if x != nil {
			return 0, x
		}
		if messager.PlaceholderIsStale(s, now) {
			if e = r.expirePlaceholderTx(ctx, tx, v); e != nil {
				return 0, e
			}
			n++
		}
	}
	return n, tx.Commit()
}
