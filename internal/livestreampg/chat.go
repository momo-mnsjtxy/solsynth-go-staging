// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

//go:embed chat_schema.sql
var ChatSchema string

type ChatConfig struct {
	MaxMessagesPerStream, MaxEventsPerStream, MaxAttempts int
	EventTTL, SendTimeout                                 time.Duration
}

var _ live.ChatRepository = (*Repository)(nil)

func (r *Repository) WithChat(c ChatConfig) error {
	if r == nil || c.MaxMessagesPerStream < 1 || c.MaxMessagesPerStream > 100000 || c.MaxEventsPerStream < c.MaxMessagesPerStream || c.MaxEventsPerStream > 200000 || c.MaxAttempts < 1 || c.MaxAttempts > 20 || c.EventTTL < time.Minute || c.EventTTL > 24*time.Hour || c.SendTimeout <= 0 || c.SendTimeout > 5*time.Second {
		return live.ErrInvalid
	}
	if _, ok := r.provider.(live.DataProvider); !ok {
		return live.ErrUnconfigured
	}
	if _, ok := r.publishers.(live.ChatAccountProvider); !ok {
		return live.ErrUnavailable
	}
	r.chat = &c
	return nil
}
func (r *Repository) chatConfigured() error {
	if r == nil || r.chat == nil {
		return live.ErrUnsupported
	}
	if e := r.configured(); e != nil {
		return e
	}
	return nil
}
func (r *Repository) ChatReady(ctx context.Context) error {
	if e := r.chatConfigured(); e != nil {
		return e
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,sender_id,timeout_until FROM owned_livestream_chat_messages LIMIT 0;SELECT id,state,expires_at FROM owned_livestream_chat_outbox LIMIT 0`)
	return e
}

const chatFields = `m.id::text,m.stream_id::text,m.sender_id::text,m.sender_name,m.content,m.timeout_until,m.created_at,m.updated_at,m.deleted_at,COALESCE(o.state,'pending')`

func chatScan(row scanner) (m live.ChatMessage, e error) {
	e = row.Scan(&m.ID, &m.LiveStreamID, &m.SenderID, &m.SenderName, &m.Content, &m.TimeoutUntil, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt, &m.DeliveryState)
	if errors.Is(e, sql.ErrNoRows) {
		e = live.ErrNotFound
	}
	return
}
func chatMessage(ctx context.Context, tx *sql.Tx, sid, id string) (live.ChatMessage, error) {
	return chatScan(tx.QueryRowContext(ctx, `SELECT `+chatFields+` FROM owned_livestream_chat_messages m LEFT JOIN owned_livestream_chat_outbox o ON o.message_id=m.id AND o.kind='message' WHERE m.stream_id=$1 AND m.id=$2`, sid, id))
}
func (r *Repository) chatSender(ctx context.Context, tx *sql.Tx, viewer string, m *live.ChatMessage) error {
	p, ok := r.publishers.(live.ChatAccountProvider)
	if !ok {
		return live.ErrUnavailable
	}
	a, e := p.AccountInTransaction(ctx, tx, viewer, m.SenderID)
	if errors.Is(e, accounts.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	m.Sender = &a
	return nil
}
func (r *Repository) ListChat(ctx context.Context, actor, sid string, limit, offset int) ([]live.ChatMessage, error) {
	if e := r.chatConfigured(); e != nil {
		return nil, e
	}
	if !authority.ValidID(actor) {
		return nil, live.ErrUnauthorized
	}
	if !authority.ValidID(sid) || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, live.ErrInvalid
	}
	tx, _, v, e := r.locked(ctx, actor, sid, "", "livestreams.view", false, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+chatFields+` FROM owned_livestream_chat_messages m LEFT JOIN owned_livestream_chat_outbox o ON o.message_id=m.id AND o.kind='message' WHERE m.stream_id=$1 AND m.deleted_at IS NULL ORDER BY m.created_at DESC,m.id LIMIT $2 OFFSET $3`, sid, limit, offset)
	if e != nil {
		return nil, e
	}
	out := []live.ChatMessage{}
	for rows.Next() {
		m, x := chatScan(rows)
		if x != nil {
			rows.Close()
			return nil, x
		}
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		if e = r.chatSender(ctx, tx, actor, &out[i]); e != nil {
			return nil, e
		}
	}
	return out, r.commit(ctx, tx, actor, "livestreams.view")
}
func chatKey(key string) error {
	if len(key) > 128 || !live.Text(key, 128) {
		return live.ErrInvalid
	}
	return nil
}
func (r *Repository) eventCapacity(ctx context.Context, tx *sql.Tx, sid string) error {
	var n int
	if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_livestream_chat_outbox WHERE stream_id=$1`, sid).Scan(&n); e != nil {
		return e
	}
	if n >= r.chat.MaxEventsPerStream {
		return live.ErrConflict
	}
	return nil
}
func (r *Repository) enqueueChat(ctx context.Context, tx *sql.Tx, v run, p authority.Principal, id, message, kind, key string, fingerprint, payload []byte, at time.Time) error {
	if len(payload) > live.MaxDataPacketBytes {
		return live.ErrChatPacket
	}
	if e := r.eventCapacity(ctx, tx, v.Stream); e != nil {
		return e
	}
	actor := p.Account.ID
	permission := "livestreams.chat.send"
	if kind == "timeout" {
		permission = "livestreams.chat.moderate"
	}
	var keyHash any
	if key != "" {
		keyHash = hash(key)
	}
	var mid any
	if message != "" {
		mid = message
	}
	expiry := at.Add(r.chat.EventTTL)
	if expiry.After(v.ExpiresAt) {
		expiry = v.ExpiresAt
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_livestream_chat_outbox(id,stream_id,generation,actor_id,message_id,kind,payload,request_key_hash,request_hash,created_at,next_attempt_at,expires_at,session_id,account_version,session_epoch,permission)VALUES($1::uuid,$2::uuid,$3::bigint,$4::uuid,$5::uuid,$6::text,$7::bytea,$8::bytea,$9::bytea,$10::timestamptz,$10::timestamptz,$11::timestamptz,$12::uuid,$13::bigint,$14::bigint,$15::text)`, id, v.Stream, v.Generation, actor, mid, kind, payload, keyHash, fingerprint, at, expiry, p.Session.ID, p.Account.Version, p.Session.Epoch, permission)
	return e
}
func (r *Repository) SendChat(ctx context.Context, actor, sid, content, key string) (out live.ChatMessage, e error) {
	if e = r.chatConfigured(); e != nil {
		return
	}
	if e = chatKey(key); e != nil {
		return
	}
	if !live.Text(content, 4096) || strings.TrimSpace(content) == "" {
		return out, live.ErrInvalid
	}
	tx, p, v, e := r.locked(ctx, actor, sid, "", "livestreams.chat.send", false, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return out, e
	}
	if v.Status != live.Active || v.OperationState != "active" {
		return out, live.ErrConflict
	}
	run, e := loadRun(ctx, tx, sid, v.Generation)
	if e != nil {
		return out, e
	}
	alive, e := r.runAlive(ctx, tx, run)
	if e != nil {
		return out, e
	}
	if !alive {
		return out, live.ErrForbidden
	}
	fingerprint := hash(struct {
		Generation int64
		Content    string
	}{v.Generation, content})
	if key != "" {
		var prior string
		var saved []byte
		e = tx.QueryRowContext(ctx, `SELECT id::text,request_hash FROM owned_livestream_chat_messages WHERE stream_id=$1 AND sender_id=$2 AND request_key_hash=$3`, sid, actor, hash(key)).Scan(&prior, &saved)
		if e == nil {
			if !bytes.Equal(saved, fingerprint) {
				return out, live.ErrConflict
			}
			out, e = chatMessage(ctx, tx, sid, prior)
			if e != nil {
				return out, e
			}
			if out.DeletedAt != nil {
				return out, live.ErrConflict
			}
			if e = r.chatSender(ctx, tx, actor, &out); e != nil {
				return out, e
			}
			return out, r.commit(ctx, tx, actor, "livestreams.chat.send")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
	}
	var muted bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_livestream_chat_messages WHERE stream_id=$1 AND sender_id=$2 AND timeout_until>clock_timestamp())`, sid, actor).Scan(&muted)
	if e != nil {
		return out, e
	}
	if muted {
		return out, live.ErrChatMuted
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_livestream_chat_messages WHERE stream_id=$1`, sid).Scan(&count); e != nil {
		return out, e
	}
	if count >= r.chat.MaxMessagesPerStream {
		return out, live.ErrConflict
	}
	id, e := uid()
	if e != nil {
		return out, e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	name := p.Account.Nick
	if name == "" {
		name = p.Account.Name
	}
	if name == "" {
		name = "Anonymous"
	}
	runes := []rune(name)
	if len(runes) > 128 {
		name = string(runes[:128])
	}
	out = live.ChatMessage{ID: id, LiveStreamID: sid, SenderID: actor, SenderName: name, Content: content, CreatedAt: at, UpdatedAt: at, DeliveryState: "pending"}
	if e = r.chatSender(ctx, tx, actor, &out); e != nil {
		return out, e
	}
	wire := out
	wire.DeliveryState = ""
	if wire.Sender != nil {
		copy := *wire.Sender
		copy.Profile = nil
		wire.Sender = &copy
	}
	payload, e := json.Marshal(wire)
	if e != nil {
		return out, e
	}
	if len(payload) > live.MaxDataPacketBytes {
		return out, live.ErrChatPacket
	}
	var keyHash any
	if key != "" {
		keyHash = hash(key)
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_chat_messages(id,stream_id,generation,sender_id,sender_name,content,created_at,updated_at,request_key_hash,request_hash)VALUES($1::uuid,$2::uuid,$3::bigint,$4::uuid,$5::text,$6::text,$7::timestamptz,$7::timestamptz,$8::bytea,$9::bytea)`, id, sid, v.Generation, actor, name, content, at, keyHash, fingerprint)
	if e != nil {
		return out, e
	}
	if e = r.enqueueChat(ctx, tx, run, p, id, id, "message", key, fingerprint, payload, at); e != nil {
		return out, e
	}
	if e = audit(ctx, tx, sid, actor, "livestreams.chat.send"); e != nil {
		return out, e
	}
	return out, r.commit(ctx, tx, actor, "livestreams.chat.send")
}

// This nonlocking observation closes before the mutation transaction begins.
// It reveals no data before original-proof permission and current Editor checks.
// The immutable sender is then included in the complete sorted lock plan and
// verified again under the stream lock; no late target account lock is taken.
func (r *Repository) chatTarget(ctx context.Context, actor, sid, mid string) (string, error) {
	if !authority.ValidID(actor) || !authority.ValidID(sid) || !authority.ValidID(mid) {
		return "", live.ErrInvalid
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	p, e := r.guard.Preflight(ctx, r.db, tx, actor, "livestreams.chat.moderate")
	if e != nil {
		return "", e
	}
	if p.Session.Type != 0 || p.Session.ParentID != "" {
		return "", authority.ErrInvalid
	}
	v, e := get(ctx, tx, sid, false, false)
	if e != nil {
		return "", e
	}
	editor, e := r.visible(ctx, tx, actor, &v)
	if e != nil {
		return "", e
	}
	if !editor {
		return "", live.ErrForbidden
	}
	m, e := chatMessage(ctx, tx, sid, mid)
	if e != nil {
		return "", e
	}
	return m.SenderID, tx.Commit()
}
func (r *Repository) DeleteChat(ctx context.Context, actor, sid, mid string) error {
	if e := r.chatConfigured(); e != nil {
		return e
	}
	target, e := r.chatTarget(ctx, actor, sid, mid)
	if e != nil {
		return e
	}
	tx, _, stream, e := r.locked(ctx, actor, sid, "", "livestreams.chat.moderate", true, false, target)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.visible(ctx, tx, actor, &stream); e != nil {
		return e
	}
	m, e := chatMessage(ctx, tx, sid, mid)
	if e != nil {
		return e
	}
	if m.SenderID != target {
		return live.ErrConflict
	}
	if m.DeletedAt == nil {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_chat_messages SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, mid); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET state='cancelled',reason='message_deleted' WHERE message_id=$1 AND kind='message' AND state='pending'`, mid); e != nil {
			return e
		}
		if e = audit(ctx, tx, sid, actor, "livestreams.chat.delete"); e != nil {
			return e
		}
	}
	return r.commit(ctx, tx, actor, "livestreams.chat.moderate")
}
func (r *Repository) TimeoutChat(ctx context.Context, actor, sid, mid, key string, q live.ChatTimeout) error {
	if e := r.chatConfigured(); e != nil {
		return e
	}
	if e := chatKey(key); e != nil {
		return e
	}
	minutes := 10
	if q.DurationMinutes != nil {
		minutes = *q.DurationMinutes
	}
	if minutes < 0 || minutes > 10080 {
		return live.ErrInvalid
	}
	target, e := r.chatTarget(ctx, actor, sid, mid)
	if e != nil {
		return e
	}
	tx, p, stream, e := r.locked(ctx, actor, sid, "", "livestreams.chat.moderate", true, false, target)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.visible(ctx, tx, actor, &stream); e != nil {
		return e
	}
	m, e := chatMessage(ctx, tx, sid, mid)
	if e != nil {
		return e
	}
	if m.SenderID != target {
		return live.ErrConflict
	}
	fingerprint := hash(struct {
		Message string
		Minutes int
	}{mid, minutes})
	if key != "" {
		var saved []byte
		e = tx.QueryRowContext(ctx, `SELECT request_hash FROM owned_livestream_chat_outbox WHERE stream_id=$1 AND actor_id=$2 AND kind='timeout' AND request_key_hash=$3`, sid, actor, hash(key)).Scan(&saved)
		if e == nil {
			if !bytes.Equal(saved, fingerprint) {
				return live.ErrConflict
			}
			return r.commit(ctx, tx, actor, "livestreams.chat.moderate")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	at, e := now(ctx, tx)
	if e != nil {
		return e
	}
	deadline := at.Add(time.Duration(minutes) * time.Minute)
	event, e := uid()
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_chat_messages SET timeout_until=$2::timestamptz,updated_at=$3::timestamptz WHERE id=$1`, mid, deadline, at); e != nil {
		return e
	}
	v, e := loadRun(ctx, tx, sid, stream.Generation)
	if e != nil {
		return e
	}
	payload, e := json.Marshal(map[string]any{"type": "timeout", "event_id": event, "sender_id": target, "duration_minutes": minutes, "timeout_until": deadline})
	if e != nil {
		return e
	}
	if e = r.enqueueChat(ctx, tx, v, p, event, mid, "timeout", key, fingerprint, payload, at); e != nil {
		return e
	}
	if e = audit(ctx, tx, sid, actor, "livestreams.chat.timeout"); e != nil {
		return e
	}
	return r.commit(ctx, tx, actor, "livestreams.chat.moderate")
}
