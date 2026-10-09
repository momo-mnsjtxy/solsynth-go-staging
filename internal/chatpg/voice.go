// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"time"

	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type VoiceStorage interface {
	PrepareVoice(context.Context, string, string, rooms.Member, rooms.VoiceRequest, []byte, time.Duration) (rooms.VoiceClip, error)
	BindVoice(context.Context, *sql.Tx, string, string, rooms.Member, string, string) (rooms.VoiceClip, error)
	OpenVoiceInTransaction(context.Context, *sql.Tx, string, string, string) (rooms.VoiceClip, io.ReadSeekCloser, error)
	AbandonVoice(context.Context, string) error
	VoiceReady(context.Context) error
}

// Retention is an explicit owned policy: retained sources establish cleanup but
// do not establish the original storage service's retention or size settings.
func (r *Repository) WithVoiceStorage(v VoiceStorage, retention time.Duration) error {
	if v == nil || retention < time.Hour || retention > 365*24*time.Hour {
		return rooms.ErrInvalid
	}
	r.voice = v
	r.voiceRetention = retention
	return nil
}
func (r *Repository) voiceReferences(ctx context.Context, tx *sql.Tx, actor, rid string, p rooms.VoiceRequest) error {
	if p.RepliedMessageID != nil {
		var yes bool
		e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages m JOIN chat_members s ON s.id=m.sender_id WHERE m.id=$1 AND m.chat_room_id=$2 AND m.type IN('text','voice') AND m.deleted_at IS NULL AND s.deleted_at IS NULL AND (m.encryption_meta IS NULL OR m.encryption_meta='null'::jsonb))`, *p.RepliedMessageID, rid).Scan(&yes)
		if e != nil {
			return e
		}
		if !yes {
			return rooms.ErrNotFound
		}
	}
	if p.ForwardedMessageID != nil {
		if _, e := r.sourceMessage(ctx, tx, actor, *p.ForwardedMessageID, false); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) SendVoice(ctx context.Context, actor, rid string, p rooms.VoiceRequest) (messager.MessageProjection, error) {
	var out messager.MessageProjection
	if e := valid(actor, rid); e != nil {
		return out, e
	}
	var e error
	p, e = rooms.ValidateVoice(p)
	if e != nil {
		return out, e
	}
	if r == nil || r.voice == nil {
		return out, rooms.ErrUnavailable
	}
	// Authorize the room before reserving or writing bytes, then revalidate the
	// original request proof, room, membership incarnation and source after I/O.
	tx, e := r.beginMessageWrite(ctx, actor, rid, p.ForwardedMessageID)
	if e != nil {
		return out, e
	}
	sender, e := r.plaintextWritable(ctx, tx, actor, rid)
	if e == nil {
		e = r.RequireRoomMediaInTransaction(ctx, tx, actor, rid)
	}
	if e == nil {
		e = r.voiceReferences(ctx, tx, actor, rid, p)
	}
	if e != nil {
		tx.Rollback()
		return out, e
	}
	if e = r.commit(ctx, tx, actor, "chat.messages.create"); e != nil {
		tx.Rollback()
		return out, e
	}
	fileDigest := sha256.Sum256(p.Data)
	raw, _ := json.Marshal(struct {
		Name, MIME     string
		Size           int
		Digest         [32]byte
		Duration       *int32
		Reply, Forward *string
	}{p.Name, p.MIMEType, len(p.Data), fileDigest, p.DurationMS, p.RepliedMessageID, p.ForwardedMessageID})
	requestHash := sha256.Sum256(raw)
	clip, e := r.voice.PrepareVoice(ctx, actor, rid, sender, p, requestHash[:], r.voiceRetention)
	if e != nil {
		return out, e
	}
	// Compensation is conditional on an unbound durable receipt. It cannot erase
	// a committed message after an uncertain commit or a successful concurrent retry.
	success := false
	defer func() {
		if !success {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = r.voice.AbandonVoice(cleanup, clip.ID)
		}
	}()
	tx, e = r.beginMessageWrite(ctx, actor, rid, p.ForwardedMessageID)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	current, e := r.plaintextWritable(ctx, tx, actor, rid)
	if e != nil {
		return out, e
	}
	if current.JoinedAt == nil || sender.JoinedAt == nil || current.ID != sender.ID || !current.JoinedAt.Equal(*sender.JoinedAt) {
		return out, rooms.ErrConflict
	}
	if e = r.RequireRoomMediaInTransaction(ctx, tx, actor, rid); e != nil {
		return out, e
	}
	if e = r.voiceReferences(ctx, tx, actor, rid, p); e != nil {
		return out, e
	}
	if clip.MessageID != nil {
		out, e = textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND sender_id=$3 AND type='voice' AND deleted_at IS NULL`, *clip.MessageID, rid, current.ID))
		if e != nil {
			return out, e
		}
	} else {
		// Another retry may have bound this same clip between prepare and room lock.
		var prior *string
		if e = tx.QueryRowContext(ctx, `SELECT message_id::text FROM owned_chat_voice_clips WHERE id=$1`, clip.ID).Scan(&prior); e != nil {
			return out, e
		}
		if prior != nil {
			out, e = textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2 AND sender_id=$3 AND type='voice' AND deleted_at IS NULL`, *prior, rid, current.ID))
		} else {
			out, e = appendMessage(ctx, tx, rid, current.ID, "voice", nil, p.ClientMessageID, p.RepliedMessageID, rooms.VoiceMeta(clip), requestHash[:], nil)
			if e == nil && p.ForwardedMessageID != nil {
				out, e = textMessage(tx.QueryRowContext(ctx, `UPDATE chat_messages SET forwarded_message_id=$2 WHERE id=$1 RETURNING `+textColumns, out.ID, p.ForwardedMessageID))
			}
		}
		if e != nil {
			return out, e
		}
	}
	if _, e = r.voice.BindVoice(ctx, tx, actor, rid, current, clip.ID, out.ID); e != nil {
		return out, e
	}
	if e = r.loadMessage(ctx, tx, actor, &out); e != nil {
		return out, e
	}
	if e = r.voiceReferences(ctx, tx, actor, rid, p); e != nil {
		return out, e
	}
	if _, e = r.plaintextWritable(ctx, tx, actor, rid); e != nil {
		return out, e
	}
	if e = r.RequireRoomMediaInTransaction(ctx, tx, actor, rid); e != nil {
		return out, e
	}
	if _, e = r.voice.BindVoice(ctx, tx, actor, rid, current, clip.ID, out.ID); e != nil {
		return out, e
	}
	e = r.commit(ctx, tx, actor, "chat.messages.create")
	success = e == nil
	return out, e
}
func (r *Repository) OpenVoice(ctx context.Context, actor, rid, vid string) (rooms.VoiceClip, io.ReadSeekCloser, error) {
	var clip rooms.VoiceClip
	if e := valid(rid, vid); e != nil {
		return clip, nil, e
	}
	if actor != "" {
		if e := valid(actor); e != nil {
			return clip, nil, e
		}
	}
	if r == nil || r.voice == nil {
		return clip, nil, rooms.ErrUnavailable
	}
	var tx *sql.Tx
	var e error
	if actor != "" {
		tx, e = r.beginRoomWrite(ctx, actor, rid, "", "")
	} else {
		tx, e = r.begin(ctx, false)
	}
	if e != nil {
		return clip, nil, e
	}
	defer tx.Rollback()
	check := func() error {
		v, e := r.room(ctx, tx, rid, false)
		if e != nil {
			return e
		}
		if v.EncryptionMode != r.policy.NoneEncryptionMode {
			return rooms.ErrUnsupported
		}
		if e = r.readable(ctx, tx, v, actor); e != nil {
			return e
		}
		if actor != "" && !r.fixture {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		}
		return e
	}
	if e = check(); e != nil {
		return clip, nil, e
	}
	clip, body, e := r.voice.OpenVoiceInTransaction(ctx, tx, actor, rid, vid)
	if e != nil {
		return clip, nil, e
	}
	if e = check(); e != nil {
		body.Close()
		return clip, nil, e
	}
	if e = tx.Commit(); e != nil {
		body.Close()
		return clip, nil, e
	}
	return clip, body, nil
}
