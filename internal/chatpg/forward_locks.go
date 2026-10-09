// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
)

// Gather every associated account/realm before taking any room lock. Opposing
// forwards share the exact canonical account -> realm -> sorted-room order.
func (r *Repository) beginMessageWrite(ctx context.Context, actor, destination string, forward *string) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, rooms.ErrUnavailable
	}
	if forward == nil {
		return r.beginRoomWrite(ctx, actor, destination, "chat.messages.create", "")
	}
	tx, _, e := r.beginForwardWrite(ctx, actor, destination, *forward)
	return tx, e
}
func (r *Repository) beginForwardWrite(ctx context.Context, actor, destination, message string) (tx *sql.Tx, source string, e error) {
	if r == nil || (!r.fixture && r.guard == nil) {
		return nil, "", rooms.ErrUnavailable
	}
	tx, e = r.begin(ctx, false)
	if e != nil {
		return nil, "", e
	}
	fail := func(err error) (*sql.Tx, string, error) { _ = tx.Rollback(); return nil, "", err }
	if !r.fixture {
		if e = r.preflight(ctx, tx, actor, "chat.messages.create"); e != nil {
			return fail(e)
		}
	}
	// Source existence is domain information. Resolve it only after validating
	// the original bound proof, in the transaction that will take the locks.
	if e = tx.QueryRowContext(ctx, `SELECT chat_room_id::text FROM chat_messages WHERE id=$1`, message).Scan(&source); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			e = rooms.ErrNotFound
		}
		return fail(e)
	}
	roomIDs := dedup([]string{destination, source})
	accounts, realms := []string{actor}, []string{}
	type association struct{ accounts, realms []string }
	initial := map[string]association{}
	for _, rid := range roomIDs {
		if !rooms.ValidID(rid) {
			return fail(rooms.ErrInvalid)
		}
		ids, rs, e := r.associated(ctx, tx, rid)
		if e != nil {
			return fail(e)
		}
		initial[rid] = association{ids, rs}
		accounts = append(accounts, ids...)
		realms = append(realms, rs...)
	}
	accounts = dedup(accounts)
	for _, id := range accounts {
		var found string
		if !rooms.ValidID(id) {
			return fail(rooms.ErrInvalid)
		}
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&found); e != nil {
			return fail(e)
		}
	}
	if !r.fixture {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "chat.messages.create"); e != nil {
			return fail(e)
		}
	}
	for _, id := range dedup(realms) {
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&owner); e != nil {
			return fail(rooms.ErrNotFound)
		}
		if !hasID(accounts, owner) {
			return fail(rooms.ErrConflict)
		}
	}
	for _, rid := range roomIDs {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, rid).Scan(&found); e != nil {
			return fail(rooms.ErrNotFound)
		}
		ids, rs, e := r.associated(ctx, tx, rid)
		if e != nil {
			return fail(e)
		}
		prior := initial[rid]
		if len(ids) != len(prior.accounts) || len(rs) != len(prior.realms) {
			return fail(rooms.ErrConflict)
		}
		for _, id := range ids {
			if !hasID(prior.accounts, id) {
				return fail(rooms.ErrConflict)
			}
		}
		for _, id := range rs {
			if !hasID(prior.realms, id) {
				return fail(rooms.ErrConflict)
			}
		}
	}
	return tx, source, nil
}
func (r *Repository) sourceMessage(ctx context.Context, tx *sql.Tx, actor, mid string, memberRequired bool) (v sourceRecord, e error) {
	v.message, e = textMessage(tx.QueryRowContext(ctx, `SELECT `+textColumns+` FROM chat_messages WHERE id=$1 AND type='text' AND deleted_at IS NULL FOR SHARE`, mid))
	if e != nil {
		return v, e
	}
	room, e := r.room(ctx, tx, v.message.ChatRoomID, false)
	if e != nil {
		return v, e
	}
	if room.EncryptionMode != r.policy.NoneEncryptionMode || v.message.EncryptionMeta != nil && string(v.message.EncryptionMeta) != "null" {
		return v, rooms.ErrUnsupported
	}
	if e = r.readable(ctx, tx, room, actor); e != nil {
		return v, e
	}
	if memberRequired {
		v.membership, e = requireMember(ctx, tx, room.ID, actor)
		if e != nil {
			return v, e
		}
	}
	var live bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_members WHERE id=$1 AND chat_room_id=$2 AND deleted_at IS NULL)`, v.message.SenderID, room.ID).Scan(&live); e != nil {
		return v, e
	}
	if !live {
		return v, rooms.ErrNotFound
	}
	return
}
