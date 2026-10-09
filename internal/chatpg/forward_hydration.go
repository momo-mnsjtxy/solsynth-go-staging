// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type sourceRecord struct {
	message    messager.MessageProjection
	membership rooms.Member
}

// Load composes canonical media with live references for both ordinary and
// thread readers. A preview never copies private metadata, files or cryptography.
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, v *messager.MessageProjection) error {
	if r.associations != nil {
		if e := r.associations.Load(ctx, tx, actor, v); e != nil {
			return e
		}
	}
	return r.HydrateReferences(ctx, tx, actor, v)
}
func (r *Repository) HydrateReferences(ctx context.Context, tx *sql.Tx, actor string, v *messager.MessageProjection) error {
	v.ForwardedMessage = nil
	v.ForwardUnavailable = false
	if v.ForwardedMessageID == nil && !bytes.Contains(v.Meta, []byte(`"redirect"`)) {
		return nil
	}
	meta := map[string]json.RawMessage{}
	_ = json.Unmarshal(v.Meta, &meta)
	if meta == nil {
		meta = map[string]json.RawMessage{}
	}
	var snapshot []byte
	e := tx.QueryRowContext(ctx, `SELECT snapshot FROM owned_chat_redirects WHERE message_id=$1`, v.ID).Scan(&snapshot)
	if e == nil {
		meta["redirect"] = snapshot
		v.Meta, _ = json.Marshal(meta)
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	// Old arbitrary client metadata cannot masquerade as server-authored history.
	delete(meta, "redirect")
	if len(meta) == 0 {
		v.Meta = json.RawMessage(`null`)
	} else {
		v.Meta, _ = json.Marshal(meta)
	}
	if v.ForwardedMessageID == nil {
		return nil
	}
	preview, e := r.forwardPreview(ctx, tx, actor, *v.ForwardedMessageID, 0, map[string]bool{})
	if e != nil {
		return e
	}
	v.ForwardedMessage = preview
	if preview == nil {
		v.ForwardedMessageID = nil
		v.ForwardUnavailable = true
	}
	return nil
}
func (r *Repository) forwardPreview(ctx context.Context, tx *sql.Tx, actor, mid string, depth int, seen map[string]bool) (*messager.ForwardPreview, error) {
	if depth >= 4 || seen[mid] {
		return nil, nil
	}
	seen[mid] = true
	var m messager.MessageProjection
	m, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND type='text' AND deleted_at IS NULL`, mid))
	if errors.Is(e, rooms.ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	room, e := r.room(ctx, tx, m.ChatRoomID, false)
	if errors.Is(e, rooms.ErrNotFound) || errors.Is(e, rooms.ErrUnsupported) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if room.EncryptionMode != r.policy.NoneEncryptionMode || m.EncryptionMeta != nil && string(m.EncryptionMeta) != "null" {
		return nil, nil
	}
	if e = r.readable(ctx, tx, room, actor); errors.Is(e, rooms.ErrForbidden) || errors.Is(e, rooms.ErrNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var live bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_members WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL)`, m.SenderID, m.ChatRoomID).Scan(&live); e != nil {
		return nil, e
	}
	if !live {
		return nil, nil
	}
	out := &messager.ForwardPreview{ID: m.ID, ChatRoomID: m.ChatRoomID, SenderID: m.SenderID, Content: messager.FormatMessageContent(m.Content, false), CreatedAt: m.CreatedAt}
	if m.ForwardedMessageID != nil {
		out.ForwardedMessage, e = r.forwardPreview(ctx, tx, actor, *m.ForwardedMessageID, depth+1, seen)
		if e != nil {
			return nil, e
		}
		out.ForwardUnavailable = out.ForwardedMessage == nil
	}
	return out, nil
}
