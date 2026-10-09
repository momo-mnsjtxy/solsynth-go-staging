// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager/rooms"
)

//go:embed voice_schema.sql
var VoiceSchema string

func (r *Repository) VoiceReady(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT id,room_id,sender_id,account_id,sender_joined_at,message_id,request_hash,duration_ms,created_at,pending_expires_at,expires_at FROM owned_chat_voice_clips LIMIT 0`)
	return e
}

const clipFields = `c.id::text,c.room_id::text,c.sender_id::text,c.account_id::text,c.sender_joined_at,c.message_id::text,c.duration_ms,c.created_at,c.expires_at,f.name,f.mime_type,f.size`

func scanClip(s scanner) (c rooms.VoiceClip, e error) {
	e = s.Scan(&c.ID, &c.RoomID, &c.SenderID, &c.AccountID, &c.JoinedAt, &c.MessageID, &c.DurationMS, &c.CreatedAt, &c.ExpiresAt, &c.Name, &c.MIMEType, &c.Size)
	c.FileID = c.ID
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	return
}

// A staging receipt and quota reservation commit before object I/O. Crashes and
// uncertain chat commits therefore leave a durable cleanup target; bound clips
// can never be removed by a failing retry's compensation.
func (r *Repository) PrepareVoice(ctx context.Context, actor, rid string, sender rooms.Member, p rooms.VoiceRequest, requestHash []byte, retention time.Duration) (rooms.VoiceClip, error) {
	var result rooms.VoiceClip
	var e error
	p, e = rooms.ValidateVoice(p)
	if e != nil {
		return result, e
	}
	if !rooms.ValidID(actor) || !rooms.ValidID(rid) || !rooms.ValidID(sender.ID) || sender.JoinedAt == nil || len(requestHash) != 32 || retention < time.Hour || retention > 365*24*time.Hour {
		return result, rooms.ErrInvalid
	}
	key, e := uuid()
	if e != nil {
		return result, e
	}
	if p.ClientMessageID != nil {
		key = *p.ClientMessageID
	}
	keyHash := sha256.Sum256([]byte(rid + ":" + key))
	digest := sha256.Sum256(p.Data)
	u := media.Upload{Name: p.Name, MIMEType: p.MIMEType, Size: int64(len(p.Data)), SHA256: hex.EncodeToString(digest[:]), IdempotencyKey: "chatvoice:" + hex.EncodeToString(keyHash[:])}
	if e = validUpload(u, r.cfg.MaxFileBytes); e != nil {
		return result, e
	}
	tx, e := r.begin(ctx, actor, media.CreatePermission, "chat.messages.create")
	if e != nil {
		return result, e
	}
	defer tx.Rollback()
	f, replayed, e := r.reserveTx(ctx, tx, actor, u)
	if e != nil {
		return result, e
	}
	if replayed {
		var stored []byte
		e = tx.QueryRowContext(ctx, `SELECT request_hash FROM owned_chat_voice_clips WHERE id=$1`, f.ID).Scan(&stored)
		if errors.Is(e, sql.ErrNoRows) {
			return result, rooms.ErrConflict
		}
		if e != nil {
			return result, e
		}
		if !bytes.Equal(stored, requestHash) {
			return result, rooms.ErrConflict
		}
		result, e = scanClip(tx.QueryRowContext(ctx, `SELECT `+clipFields+` FROM owned_chat_voice_clips c JOIN owned_media_files f ON f.id=c.id WHERE c.id=$1 AND c.room_id=$2 AND c.sender_id=$3 AND c.account_id=$4 AND c.sender_joined_at=$5 AND c.expires_at>clock_timestamp()`, f.ID, rid, sender.ID, actor, *sender.JoinedAt))
		if e != nil {
			return result, e
		}
	} else {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_voice_clips(id,room_id,sender_id,account_id,sender_joined_at,request_hash,duration_ms,created_at,pending_expires_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp(),clock_timestamp()+($8::bigint*interval '1 millisecond'),clock_timestamp()+($9::bigint*interval '1 millisecond'))`, f.ID, rid, sender.ID, actor, *sender.JoinedAt, requestHash, p.DurationMS, r.cfg.PendingTTL.Milliseconds(), retention.Milliseconds())
		if e != nil {
			return result, e
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission, "chat.messages.create"); e != nil {
		return result, e
	}
	if e = tx.Commit(); e != nil {
		return result, e
	}
	if !replayed {
		f, e = r.finish(ctx, actor, f, bytes.NewReader(p.Data), false, "chat.messages.create")
		if e != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = r.AbandonVoice(cleanup, f.ID)
			return result, e
		}
		result, e = scanClip(r.db.QueryRowContext(ctx, `SELECT `+clipFields+` FROM owned_chat_voice_clips c JOIN owned_media_files f ON f.id=c.id WHERE c.id=$1`, f.ID))
	}
	return result, e
}
func (r *Repository) BindVoice(ctx context.Context, tx *sql.Tx, actor, rid string, sender rooms.Member, clipID, messageID string) (rooms.VoiceClip, error) {
	var result rooms.VoiceClip
	if tx == nil || sender.JoinedAt == nil {
		return result, rooms.ErrUnavailable
	}
	if _, e := r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission, "chat.messages.create"); e != nil {
		return result, e
	}
	// Room writers already hold the canonical actor/realm/room locks. Media
	// cleanup takes account -> quota -> file, and never waits on a room lock.
	result, e := scanClip(tx.QueryRowContext(ctx, `SELECT `+clipFields+` FROM owned_chat_voice_clips c JOIN owned_media_files f ON f.id=c.id WHERE c.id=$1 AND c.account_id=$2 AND c.room_id=$3 AND c.sender_id=$4 AND c.sender_joined_at=$5 AND f.state='ready' FOR UPDATE OF f,c`, clipID, actor, rid, sender.ID, *sender.JoinedAt))
	if e != nil {
		return result, e
	}
	var live bool
	e = tx.QueryRowContext(ctx, `SELECT expires_at>clock_timestamp() AND(message_id IS NOT NULL OR pending_expires_at>clock_timestamp()) FROM owned_chat_voice_clips WHERE id=$1`, clipID).Scan(&live)
	if e != nil {
		return result, e
	}
	if !live {
		return result, rooms.ErrNotFound
	}
	if result.MessageID != nil && *result.MessageID != messageID {
		return result, rooms.ErrConflict
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_chat_voice_clips SET message_id=$2 WHERE id=$1 AND(message_id IS NULL OR message_id=$2)`, clipID, messageID)
	if e == nil {
		result.MessageID = &messageID
	}
	return result, e
}

// Called inside the current room reader transaction. Caller rechecks original
// proof and current room audience after this potentially blocking object open.
func (r *Repository) OpenVoiceInTransaction(ctx context.Context, tx *sql.Tx, actor, rid, clipID string) (rooms.VoiceClip, io.ReadSeekCloser, error) {
	var result rooms.VoiceClip
	result, e := scanClip(tx.QueryRowContext(ctx, `SELECT `+clipFields+` FROM owned_chat_voice_clips c JOIN owned_media_files f ON f.id=c.id JOIN chat_messages m ON m.id=c.message_id JOIN chat_members sender ON sender.id=c.sender_id WHERE c.id=$1 AND c.room_id=$2 AND c.expires_at>clock_timestamp() AND f.state='ready' AND m.deleted_at IS NULL AND m.type='voice' AND m.chat_room_id=c.room_id AND m.sender_id=c.sender_id AND sender.deleted_at IS NULL AND sender.joined_at=c.sender_joined_at FOR SHARE OF f,c`, clipID, rid))
	if e != nil {
		return result, nil, e
	}
	if actor != "" {
		if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return result, nil, e
		}
	}
	if e = artworkOwnerActive(ctx, tx, result.AccountID); e != nil {
		return result, nil, e
	}
	f, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1 AND state='ready'`, clipID))
	if e != nil {
		return result, nil, e
	}
	body, e := r.openFile(ctx, f)
	if e != nil {
		return result, nil, e
	}
	var live bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_voice_clips c JOIN chat_messages m ON m.id=c.message_id JOIN chat_members s ON s.id=c.sender_id WHERE c.id=$1 AND c.expires_at>clock_timestamp() AND m.deleted_at IS NULL AND s.deleted_at IS NULL AND s.joined_at=c.sender_joined_at)`, clipID).Scan(&live)
	if e == nil && !live {
		e = rooms.ErrNotFound
	}
	if e == nil {
		e = artworkOwnerActive(ctx, tx, result.AccountID)
	}
	if e != nil {
		body.Close()
		return result, nil, e
	}
	return result, body, nil
}
func (r *Repository) AbandonVoice(ctx context.Context, id string) error {
	if !rooms.ValidID(id) {
		return rooms.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var actor string
	e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_chat_voice_clips WHERE id=$1`, id).Scan(&actor)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_media_files WHERE id=$1 FOR UPDATE`, id); e != nil {
		return e
	}
	// Fresh statement after all waits: a concurrent successful bind wins over
	// compensation, including when its transaction committed uncertainly.
	_, e = tx.ExecContext(ctx, `UPDATE owned_media_files f SET state='deleting',updated_at=clock_timestamp() WHERE f.id=$1 AND f.state IN('pending','ready') AND EXISTS(SELECT 1 FROM owned_chat_voice_clips c WHERE c.id=f.id AND c.message_id IS NULL)`, id)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	_, e = r.cleanupOne(ctx, id, false)
	return e
}
func (r *Repository) CleanupVoices(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, rooms.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT c.id::text,c.account_id::text FROM owned_chat_voice_clips c JOIN owned_media_files f ON f.id=c.id LEFT JOIN chat_messages m ON m.id=c.message_id JOIN chat_rooms room ON room.id=c.room_id WHERE f.state IN('ready','pending') AND(c.expires_at<=clock_timestamp() OR c.message_id IS NULL AND c.pending_expires_at<=clock_timestamp() OR m.deleted_at IS NOT NULL OR room.deleted_at IS NOT NULL) ORDER BY c.expires_at,c.id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	type candidate struct{ id, actor string }
	var list []candidate
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.id, &c.actor); e != nil {
			rows.Close()
			return 0, e
		}
		list = append(list, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	done := 0
	for _, c := range list {
		ok, e := r.expireVoice(ctx, c.id, c.actor)
		if e != nil {
			return done, e
		}
		if ok {
			done++
		}
	}
	return done, nil
}
func (r *Repository) expireVoice(ctx context.Context, id, actor string) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_media_files WHERE id=$1 FOR UPDATE`, id); e != nil {
		return false, e
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_media_files f SET state='deleting',updated_at=clock_timestamp() WHERE f.id=$1 AND f.state IN('ready','pending') AND EXISTS(SELECT 1 FROM owned_chat_voice_clips c LEFT JOIN chat_messages m ON m.id=c.message_id JOIN chat_rooms room ON room.id=c.room_id WHERE c.id=f.id AND(c.expires_at<=clock_timestamp() OR c.message_id IS NULL AND c.pending_expires_at<=clock_timestamp() OR m.deleted_at IS NOT NULL OR room.deleted_at IS NOT NULL))`, id)
	if e != nil {
		return false, e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	if n == 0 {
		return false, nil
	}
	_, e = r.cleanupOne(ctx, id, false)
	return true, e
}
