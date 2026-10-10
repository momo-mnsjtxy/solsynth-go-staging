// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accounts"
)

var _ notifications.ChatEventProjector = (*Repository)(nil)

// LiveMessage adds the source-required sender to the existing bounded owned
// message projection. It does not serialize a room member list or private reads.
type LiveMessage struct {
	messager.MessageProjection
	Sender rooms.Member `json:"sender"`
}

// MarshalJSON preserves the evidenced message/sender container while excluding
// another member's notification preferences and private read/moderation state.
// The retained authored native decoder makes these fields optional. No synthetic
// privilege, progression, missing historical timestamp or profile is invented.
func (v LiveMessage) MarshalJSON() ([]byte, error) {
	type wire LiveMessage
	raw, e := json.Marshal(wire(v))
	if e != nil {
		return nil, e
	}
	var fields map[string]json.RawMessage
	if e = json.Unmarshal(raw, &fields); e != nil {
		return nil, e
	}
	var sender map[string]json.RawMessage
	if e = json.Unmarshal(fields["sender"], &sender); e != nil {
		return nil, e
	}
	for _, key := range []string{"notify", "last_read_at", "timeout_until", "timeout_cause", "chat_room"} {
		delete(sender, key)
	}
	fields["sender"], e = json.Marshal(sender)
	if e != nil {
		return nil, e
	}
	return json.Marshal(fields)
}

func liveHidden(e error) bool {
	return errors.Is(e, sql.ErrNoRows) || errors.Is(e, rooms.ErrNotFound) || errors.Is(e, rooms.ErrForbidden) || errors.Is(e, rooms.ErrUnsupported) || errors.Is(e, accounts.ErrNotFound)
}

func liveTarget(v messager.MessageProjection) string {
	var meta struct {
		ID string `json:"message_id"`
	}
	if json.Unmarshal(v.Meta, &meta) != nil || !rooms.ValidID(meta.ID) {
		return ""
	}
	return meta.ID
}

// liveAudience checks the exact recipient incarnation, current room policy and,
// for MLS, the selected live device's current group generation and epoch. A
// device selector is account authorization, never proof of MLS key possession.
func (r *Repository) liveAudience(ctx context.Context, tx *sql.Tx, actor string, ref notifications.ChatEventRefV1, source messager.MessageProjection) (bool, error) {
	var mode int32
	e := tx.QueryRowContext(ctx, `SELECT c.encryption_mode FROM chat_members m JOIN chat_rooms c ON c.id=m.chat_room_id WHERE m.id=$1 AND m.account_id=$2 AND m.chat_room_id=$3 AND m.joined_at=$4 AND m.joined_at<=$5 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND c.deleted_at IS NULL AND c.owned_owner_id IS NOT NULL`, ref.MemberID, actor, ref.RoomID, ref.JoinedAt, source.CreatedAt).Scan(&mode)
	if liveHidden(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	allowed, e := r.CanReadInTransaction(ctx, tx, actor, ref.RoomID)
	if e != nil || !allowed {
		return false, e
	}
	device := rooms.MLSWriteFrom(ctx).Device
	if device != "" {
		if !rooms.ValidMLSIdentifier(device) {
			return false, nil
		}
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_mls_devices WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL)`, device, actor).Scan(&allowed)
		if e != nil || !allowed {
			return false, e
		}
	}
	if mode == r.policy.NoneEncryptionMode {
		return string(source.EncryptionMeta) == "null" || len(source.EncryptionMeta) == 0, nil
	}
	if !r.mls || mode != rooms.MLSEncryptionMode || device == "" {
		return false, nil
	}
	var meta rooms.EncryptionMeta
	if json.Unmarshal(source.EncryptionMeta, &meta) != nil || rooms.ValidateEncryption(&meta) != nil || meta.StateVersion == nil {
		return false, nil
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_mls_groups g JOIN owned_mls_memberships membership ON membership.group_id=g.id JOIN owned_mls_devices d ON d.id=membership.device_id WHERE g.room_id=$1 AND g.state_version=$2 AND g.epoch>=$3 AND d.id=$4 AND d.account_id=$5 AND d.revoked_at IS NULL AND membership.member_id=$6 AND membership.membership_since=$7 AND membership.state='active' AND NOT membership.reshare_required AND membership.epoch>=$3 AND(membership.expires_at IS NULL OR membership.expires_at>clock_timestamp()))`, ref.RoomID, *meta.StateVersion, meta.Epoch, device, actor, ref.MemberID, ref.JoinedAt).Scan(&allowed)
	return allowed, e
}

func (r *Repository) liveMessageCurrent(ctx context.Context, tx *sql.Tx, v messager.MessageProjection) (bool, error) {
	var live bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages m JOIN chat_members sender ON sender.id=m.sender_id JOIN owned_authority_accounts a ON a.id=sender.account_id WHERE m.id=$1 AND m.chat_room_id=$2 AND m.updated_at=$3 AND m.deleted_at IS NOT DISTINCT FROM $4::timestamptz AND sender.chat_room_id=m.chat_room_id AND sender.deleted_at IS NULL AND sender.joined_at IS NOT NULL AND sender.joined_at<=m.created_at AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())))`, v.ID, v.ChatRoomID, v.UpdatedAt, v.DeletedAt).Scan(&live)
	if e != nil || !live {
		return false, e
	}
	if v.Type == "placeholder" {
		state, e := placeholderState(v)
		if e != nil {
			return false, nil
		}
		var now time.Time
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return false, e
		}
		return state.Placeholder != nil && state.Placeholder.ExpiresAtMS != nil && *state.Placeholder.ExpiresAtMS > now.UnixMilli(), nil
	}
	if v.Type == "voice" {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_voice_clips clip JOIN owned_media_files f ON f.id=clip.id JOIN chat_members sender ON sender.id=clip.sender_id WHERE clip.message_id=$1 AND clip.room_id=$2 AND clip.expires_at>clock_timestamp() AND f.state='ready' AND sender.joined_at=clip.sender_joined_at AND sender.deleted_at IS NULL)`, v.ID, v.ChatRoomID).Scan(&live)
	}
	return live, e
}

// ProjectChatEvent runs only in the guarded stream transaction. It performs no
// external I/O. Durable storage carries references; this current projection is
// discarded on revocation, expiry, reset, deletion or recipient reincarnation.
func (r *Repository) ProjectChatEvent(ctx context.Context, tx *sql.Tx, actor string, ref notifications.ChatEventRefV1) (event notifications.Event, visible bool, err error) {
	if ref.Kind == "read" {
		return r.projectReadEvent(ctx, tx, actor, ref)
	}
	if r == nil || tx == nil || ref.Version != 1 || valid(actor, ref.RoomID, ref.MessageID, ref.MemberID) != nil || ref.JoinedAt.IsZero() {
		return event, false, notifications.ErrInvalid
	}
	source, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2`, ref.MessageID, ref.RoomID))
	if liveHidden(e) {
		return event, false, nil
	}
	if e != nil {
		return event, false, e
	}
	if source.DeletedAt != nil {
		return event, false, nil
	}
	allowed, e := r.liveAudience(ctx, tx, actor, ref, source)
	if e != nil || !allowed {
		return event, false, e
	}
	v := source
	var original *messager.MessageProjection
	event.Type = "messages.new"
	switch source.Type {
	case "text", "voice", "placeholder", "messages.delete":
	case "messages.update", "messages.sync.finalize", "messages.placeholder.update", "messages.placeholder.expired":
		target := liveTarget(source)
		if target == "" {
			return event, false, nil
		}
		current, e := textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND chat_room_id=$2`, target, ref.RoomID))
		if liveHidden(e) {
			return event, false, nil
		}
		if e != nil {
			return event, false, e
		}
		if source.Type == "messages.placeholder.expired" {
			if current.Type != "placeholder" || current.DeletedAt == nil {
				return event, false, nil
			}
			event.Type = source.Type
			event.Data, _ = json.Marshal(map[string]string{"message_id": current.ID, "chat_room_id": ref.RoomID})
			event.CreatedAt = source.CreatedAt
			allowed, e = r.liveMessageCurrent(ctx, tx, source)
			if e != nil || !allowed {
				return event, false, e
			}
			allowed, e = r.liveAudience(ctx, tx, actor, ref, source)
			return event, allowed, e
		}
		original = &current
		if current.DeletedAt != nil {
			return event, false, nil
		}
		if source.Type == "messages.placeholder.update" {
			if current.Type != "placeholder" {
				return event, false, nil
			}
			v = current
			event.Type = source.Type
		}
		// Updates/finalization cannot resurrect a now-deleted original.
		ok, e := r.liveMessageCurrent(ctx, tx, current)
		if e != nil || !ok {
			return event, false, e
		}
	default:
		return event, false, nil
	}
	sender, e := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM chat_members WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL`, v.SenderID, ref.RoomID))
	if liveHidden(e) {
		return event, false, nil
	}
	if e != nil {
		return event, false, e
	}
	publicAccount, e := accountpg.New(r.db).PublicAccountInTransaction(ctx, tx, sender.AccountID)
	sender.Account = &publicAccount
	if liveHidden(e) {
		return event, false, nil
	}
	if e != nil {
		return event, false, e
	}
	sender.LastReadAt = nil
	sender.TimeoutCause = nil
	sender.TimeoutUntil = nil
	sender.Room = nil
	encrypted := len(v.EncryptionMeta) > 0 && string(v.EncryptionMeta) != "null"
	if encrypted {
		// Opaque payloads remain opaque: no heading formatter, plaintext metadata,
		// forwarded preview, embedded links, files or inferred decrypted content.
		if rooms.ValidateMLSReferenceMeta(v.Meta) != nil && source.Type != "messages.update" && source.Type != "messages.delete" {
			return event, false, nil
		}
		v.Content = nil
		v.Meta = json.RawMessage(`null`)
		v.Attachments = json.RawMessage(`[]`)
		if source.Type == "messages.update" || source.Type == "messages.delete" {
			v.Meta, _ = json.Marshal(map[string]string{"message_id": liveTarget(source)})
		}
		v.ForwardedMessage = nil
		v.ForwardedMessageID = nil
		v.ForwardUnavailable = false
	} else {
		if e = r.loadMessage(ctx, tx, actor, &v); e != nil {
			return event, false, e
		}
		v.Content = messager.FormatMessageContent(v.Content, false)
	}
	// Never reveal an inaccessible or deleted reply merely through its ID.
	if v.RepliedMessageID != nil {
		var visible bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages m JOIN chat_members s ON s.id=m.sender_id WHERE m.id=$1 AND m.chat_room_id=$2 AND m.deleted_at IS NULL AND s.deleted_at IS NULL)`, *v.RepliedMessageID, ref.RoomID).Scan(&visible)
		if e != nil {
			return event, false, e
		}
		if !visible {
			v.RepliedMessageID = nil
		}
	}
	event.Data, e = json.Marshal(LiveMessage{v, sender})
	if e != nil {
		return event, false, e
	}
	if len(event.Data) > 256*1024 {
		return event, false, notifications.ErrUnavailable
	}
	event.CreatedAt = source.CreatedAt
	for _, check := range []messager.MessageProjection{source, v} {
		allowed, e = r.liveMessageCurrent(ctx, tx, check)
		if e != nil || !allowed {
			return event, false, e
		}
	}
	if original != nil {
		allowed, e = r.liveMessageCurrent(ctx, tx, *original)
		if e != nil || !allowed {
			return event, false, e
		}
	}
	allowed, e = r.liveAudience(ctx, tx, actor, ref, source)
	return event, allowed, e
}

func (r *Repository) projectReadEvent(ctx context.Context, tx *sql.Tx, actor string, ref notifications.ChatEventRefV1) (notifications.Event, bool, error) {
	var event notifications.Event
	if r == nil || tx == nil || ref.Version != 1 || valid(actor, ref.RoomID, ref.ReceiptID, ref.MemberID) != nil || ref.JoinedAt.IsZero() {
		return event, false, notifications.ErrInvalid
	}
	device := rooms.MLSWriteFrom(ctx).Device
	if device != "" && device == ref.ExcludedDevice {
		return event, false, nil
	}
	var owner, sourceMember, room string
	var lastRead, created time.Time
	e := tx.QueryRowContext(ctx, `SELECT account_id::text,member_id::text,room_id::text,last_read_at,created_at FROM owned_chat_read_events WHERE id=$1 AND room_id=$2`, ref.ReceiptID, ref.RoomID).Scan(&owner, &sourceMember, &room, &lastRead, &created)
	if liveHidden(e) {
		return event, false, nil
	}
	if e != nil {
		return event, false, e
	}
	var allowed bool
	connection := rooms.ChatConnectionFrom(ctx)
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_members recipient JOIN chat_rooms r ON r.id=recipient.chat_room_id WHERE recipient.id=$1 AND recipient.account_id=$2 AND recipient.chat_room_id=$3 AND recipient.joined_at=$4 AND recipient.leave_at IS NULL AND recipient.deleted_at IS NULL AND NOT recipient.owned_banned AND r.deleted_at IS NULL AND r.owned_owner_id IS NOT NULL AND($2=$5 OR(r.owned_read_receipts_public AND EXISTS(SELECT 1 FROM owned_chat_subscriptions s JOIN owned_chat_connections c ON c.id=s.connection_id WHERE s.member_id=recipient.id AND s.joined_at=recipient.joined_at AND s.expires_at>clock_timestamp() AND c.account_id=$2 AND c.closed_at IS NULL AND c.expires_at>clock_timestamp() AND($6='' OR c.id=$6::uuid)))))`, ref.MemberID, actor, room, ref.JoinedAt, owner, connection).Scan(&allowed)
	if e != nil || !allowed {
		return event, false, e
	}
	allowed, e = r.CanReadInTransaction(ctx, tx, actor, room)
	if e != nil || !allowed {
		return event, false, e
	}
	event.Type = "messages.read"
	event.CreatedAt = created
	event.Data, e = json.Marshal(map[string]any{"chat_room_id": room, "account_id": owner, "member_id": sourceMember, "last_read_at": lastRead})
	return event, e == nil, e
}
