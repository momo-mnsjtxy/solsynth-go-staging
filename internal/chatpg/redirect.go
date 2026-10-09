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
	"sort"
	"strings"
	"time"
)

// Only the authored sender attribution and text fields are retained. Private
// account/room metadata, contacts, profiles, media and cryptography are omitted.
type redirectAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Nick string `json:"nick"`
}
type redirectActor struct {
	ID        string          `json:"id"`
	AccountID string          `json:"account_id"`
	Account   redirectAccount `json:"account"`
}
type redirectEntry struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Type       string `json:"type"`
	SenderID   string `json:"sender_id"`
	ChatRoomID string `json:"chat_room_id"`
	CreatedAt  int64  `json:"created_at"`
	created    time.Time
}

func redirectIdentity(ctx context.Context, tx *sql.Tx, member, account string) (v redirectActor, e error) {
	v.ID, v.AccountID = member, account
	e = tx.QueryRowContext(ctx, `SELECT id::text,name,nick FROM owned_authority_accounts WHERE id=$1`, account).Scan(&v.Account.ID, &v.Account.Name, &v.Account.Nick)
	return
}
func buildRedirectSnapshot(source, destination string, entries []redirectEntry, senders map[string]redirectActor, actor redirectActor) map[string]any {
	out := map[string]any{"source_room_id": source, "sender_map": senders, "redirected_by": actor}
	// Only room identifiers are copied, never descriptions, ownership, realm,
	// pictures, membership preferences or other private room metadata.
	out["source_room"] = map[string]string{"id": source}
	out["redirected_to_room"] = map[string]string{"id": destination}
	first, last := entries[0], entries[len(entries)-1]
	if len(entries) == 1 {
		sender := senders[first.SenderID]
		name := sender.Account.Nick
		if name == "" {
			name = sender.Account.Name
		}
		out["version"], out["source_message_id"], out["source_message"] = 1, first.ID, first
		out["source_sender_id"], out["source_sender_name"] = sender.AccountID, name
		out["source_type"], out["source_content"], out["source_created_at"] = first.Type, first.Content, first.CreatedAt
	} else {
		out["version"], out["kind"], out["messages"] = 2, "history_segment", entries
		out["range"] = map[string]any{"start_message_id": first.ID, "end_message_id": last.ID, "message_count": len(entries), "started_at": first.CreatedAt, "ended_at": last.CreatedAt}
	}
	return out
}
func hasRedirectEmbeds(meta map[string]json.RawMessage) bool {
	raw := bytes.TrimSpace(meta["embeds"])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var entries []json.RawMessage
	return json.Unmarshal(raw, &entries) != nil || len(entries) > 0
}

// Redirect is the authored explicit-copy operation. The first supported subset
// is ordinary plaintext without attached/embedded files or nested redirects.
// It deliberately grants the destination audience a durable text copy, unlike
// the live-reference field; source deletion cannot retract an explicit copy.
func (r *Repository) Redirect(ctx context.Context, actor, destination string, ids []string, key string) (out messager.MessageProjection, e error) {
	if valid(actor, destination) != nil || len(ids) < 1 || len(ids) > 100 || len(key) > 128 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
		return out, rooms.ErrInvalid
	}
	ids = append([]string{}, ids...)
	seen := map[string]bool{}
	for _, id := range ids {
		if !rooms.ValidID(id) || seen[id] {
			return out, rooms.ErrInvalid
		}
		seen[id] = true
	}
	sort.Strings(ids)
	if r == nil || r.db == nil {
		return out, rooms.ErrUnavailable
	}
	tx, source, e := r.beginForwardWrite(ctx, actor, destination, ids[0])
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	member, e := r.writable(ctx, tx, actor, destination)
	if e != nil {
		return out, e
	}
	room, e := r.room(ctx, tx, destination, false)
	if e != nil {
		return out, e
	}
	if room.EncryptionMode != r.policy.NoneEncryptionMode {
		return out, rooms.ErrUnsupported
	}
	entries := []redirectEntry{}
	senders := map[string]redirectActor{}
	redirector, e := redirectIdentity(ctx, tx, member.ID, actor)
	if e != nil {
		return out, e
	}
	var sourceMember rooms.Member
	for _, id := range ids {
		original, e := r.sourceMessage(ctx, tx, actor, id, true)
		if e != nil {
			return out, e
		}
		m := original.message
		if m.ChatRoomID != source {
			return out, rooms.ErrInvalid
		}
		sourceMember = original.membership
		// Validate actual canonical media rows, never cached URLs or caller JSON.
		var attached bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_media WHERE message_id=$1)`, m.ID).Scan(&attached); e != nil {
			return out, e
		}
		var storedAttachments []byte
		if e = tx.QueryRowContext(ctx, `SELECT attachments FROM chat_messages WHERE id=$1`, m.ID).Scan(&storedAttachments); e != nil {
			return out, e
		}
		var rawAttachments []json.RawMessage
		if json.Unmarshal(storedAttachments, &rawAttachments) != nil {
			return out, rooms.ErrUnavailable
		}
		var meta map[string]json.RawMessage
		_ = json.Unmarshal(m.Meta, &meta)
		if attached || len(rawAttachments) > 0 || m.ForwardedMessageID != nil || hasRedirectEmbeds(meta) || len(meta["redirect"]) > 0 {
			return out, rooms.ErrUnsupported
		}
		var senderID string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM chat_members WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, m.SenderID, source).Scan(&senderID); e != nil {
			return out, rooms.ErrNotFound
		}
		sender, e := redirectIdentity(ctx, tx, m.SenderID, senderID)
		if e != nil {
			return out, e
		}
		content := ""
		if m.Content != nil {
			content = *m.Content
		}
		if strings.TrimSpace(content) == "" {
			return out, rooms.ErrUnsupported
		}
		senders[m.SenderID] = sender
		entries = append(entries, redirectEntry{ID: m.ID, Type: "text", Content: content, SenderID: m.SenderID, ChatRoomID: source, CreatedAt: m.CreatedAt.UnixMilli(), created: m.CreatedAt})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if !a.created.Equal(b.created) {
			return a.created.Before(b.created)
		}
		return a.ID < b.ID
	})
	request, _ := json.Marshal(ids)
	digest := sha256.Sum256(request)
	if key != "" {
		var mid string
		var prior []byte
		var incarnation, sourceIncarnation string
		var since, sourceSince sql.NullTime
		e = tx.QueryRowContext(ctx, `SELECT message_id::text,request_hash,destination_member_id::text,destination_joined_at,source_member_id::text,source_joined_at FROM owned_chat_redirects WHERE actor_id=$1 AND destination_room_id=$2 AND request_key=$3`, actor, destination, key).Scan(&mid, &prior, &incarnation, &since, &sourceIncarnation, &sourceSince)
		if e == nil {
			if !bytes.Equal(prior, digest[:]) || incarnation != member.ID || member.JoinedAt == nil || !since.Valid || !member.JoinedAt.Equal(since.Time) || sourceIncarnation != sourceMember.ID || sourceMember.JoinedAt == nil || !sourceSince.Valid || !sourceMember.JoinedAt.Equal(sourceSince.Time) {
				return out, rooms.ErrConflict
			}
			out, e = textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND deleted_at IS NULL`, mid))
			if e != nil {
				return out, e
			}
			if e = r.loadMessage(ctx, tx, actor, &out); e != nil {
				return out, e
			}
			if _, e = r.writable(ctx, tx, actor, destination); e != nil {
				return messager.MessageProjection{}, e
			}
			for _, id := range ids {
				if _, e = r.sourceMessage(ctx, tx, actor, id, true); e != nil {
					return messager.MessageProjection{}, e
				}
			}
			return out, r.commit(ctx, tx, actor, "chat.messages.create")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
	}
	raw, e := json.Marshal(buildRedirectSnapshot(source, destination, entries, senders, redirector))
	if e != nil || len(raw) > 2*1024*1024 {
		return out, rooms.ErrInvalid
	}
	meta, e := json.Marshal(map[string]json.RawMessage{"redirect": raw})
	if e != nil {
		return out, e
	}
	content := "Forwarded messages"
	out, e = appendMessage(ctx, tx, destination, member.ID, "text", &content, nil, nil, meta, nil, nil)
	if e != nil {
		return out, e
	}
	first := entries[0].ID
	out, e = textMessage(tx.QueryRowContext(ctx, `UPDATE chat_messages SET forwarded_message_id=$2 WHERE id=$1 RETURNING `+textColumns, out.ID, first))
	if e != nil {
		return out, e
	}
	if member.JoinedAt == nil || sourceMember.JoinedAt == nil {
		return out, rooms.ErrForbidden
	}
	var requestKey any
	if key != "" {
		requestKey = key
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_redirects(message_id,actor_id,destination_room_id,destination_member_id,destination_joined_at,source_room_id,source_member_id,source_joined_at,source_ids,snapshot,request_key,request_hash,created_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, out.ID, actor, destination, member.ID, member.JoinedAt, source, sourceMember.ID, sourceMember.JoinedAt, request, raw, requestKey, digest[:], out.CreatedAt)
	if e != nil {
		return out, e
	}
	if e = r.loadMessage(ctx, tx, actor, &out); e != nil {
		return out, e
	}
	for _, id := range ids {
		if _, e = r.sourceMessage(ctx, tx, actor, id, true); e != nil {
			return messager.MessageProjection{}, e
		}
	}
	if _, e = r.writable(ctx, tx, actor, destination); e != nil {
		return messager.MessageProjection{}, e
	}
	return out, r.commit(ctx, tx, actor, "chat.messages.create")
}
