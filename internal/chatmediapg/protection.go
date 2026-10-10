// SPDX-License-Identifier: AGPL-3.0-only
package chatmediapg

import (
	"context"
	"database/sql"

	"solsynth.local/go-migration/ports/media"
)

// WithVoiceReadProtection controls access to voice-backed files through generic
// Drive endpoints. Disabling voice never promotes an existing clip to an
// ordinary owner/public file. Unrelated files keep their original policy.
func (r *Repository) WithVoiceReadProtection(enabled bool) *Repository {
	r.voiceReads = enabled
	return r
}

// ProtectFileReadInTransaction is a mandatory restriction, separate from parent
// visibility grants. It cannot be bypassed by file ownership, public flags or
// another parent's read grant. The caller holds the ready file and rechecks this
// policy after object I/O, using the same original proof and transaction.
func (r *Repository) ProtectFileReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) error {
	if r == nil || tx == nil || r.access == nil {
		return media.ErrUnavailable
	}
	// The canonical voice reservation carries a durable domain marker even when
	// a host disables voice and therefore does not require the voice schema.
	var marked bool
	if err := tx.QueryRowContext(ctx, `SELECT idempotency_key LIKE 'chatvoice:%' FROM owned_media_files WHERE id=$1`, id).Scan(&marked); err != nil {
		return err
	}
	if marked && !r.voiceReads {
		return media.ErrNotFound
	}
	var installed bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('owned_chat_voice_clips') IS NOT NULL`).Scan(&installed); err != nil {
		return err
	}
	if !installed {
		if marked {
			return media.ErrNotFound
		}
		return nil
	}
	var protected bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_voice_clips WHERE id=$1)`, id).Scan(&protected); err != nil {
		return err
	}
	if !protected {
		if marked {
			return media.ErrNotFound
		}
		return nil
	}
	if !r.voiceReads {
		return media.ErrNotFound
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_voice_clips clip JOIN owned_media_files f ON f.id=clip.id JOIN owned_authority_accounts owner ON owner.id=f.account_id JOIN chat_messages m ON m.id=clip.message_id JOIN chat_rooms c ON c.id=clip.room_id JOIN chat_members sender ON sender.id=clip.sender_id WHERE clip.id=$2 AND clip.expires_at>clock_timestamp() AND clip.account_id=f.account_id AND m.type='voice' AND m.deleted_at IS NULL AND m.chat_room_id=clip.room_id AND m.sender_id=clip.sender_id AND sender.chat_room_id=clip.room_id AND sender.account_id=clip.account_id AND sender.joined_at=clip.sender_joined_at AND sender.deleted_at IS NULL AND `+activeFile+` AND `+r.roomPredicate()+`)`, actor, id).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return media.ErrNotFound
	}
	return nil
}
