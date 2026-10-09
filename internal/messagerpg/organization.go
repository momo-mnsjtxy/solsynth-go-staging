// SPDX-License-Identifier: AGPL-3.0-only
package messagerpg

import (
	"context"
	"database/sql"

	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

var _ rooms.OrganizationMessageReader = (*Repository)(nil)

// OrganizationMessages joins the existing canonical hydration path to the
// aggregate owner's locked transaction. It refuses a different database and
// repeats live member/room/relationship visibility for every selected row.
func (r *Repository) OrganizationMessages(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, ids []string) ([]messager.MessageProjection, error) {
	if r == nil || !r.owned || r.db != db || db == nil || tx == nil || r.roomAccess == nil {
		return nil, messager.ErrSyncUnavailable
	}
	if !rooms.ValidID(actor) || len(ids) > 1000 {
		return nil, messager.ErrSyncInvalidRequest
	}
	for _, id := range ids {
		if !rooms.ValidID(id) {
			return nil, messager.ErrSyncInvalidRequest
		}
	}
	if len(ids) == 0 {
		return []messager.MessageProjection{}, nil
	}
	where := `m.id=ANY($1::uuid[]) AND m.deleted_at IS NULL AND EXISTS(SELECT 1 FROM chat_members sender WHERE sender.id=m.sender_id AND sender.chat_room_id=m.chat_room_id AND sender.deleted_at IS NULL) AND EXISTS(SELECT 1 FROM chat_members member JOIN chat_rooms room ON room.id=member.chat_room_id WHERE room.id=m.chat_room_id AND room.deleted_at IS NULL AND member.account_id=$2::uuid AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned AND ` + r.roomAccess.ReadSQL("room.id", "$2") + `) ORDER BY m.created_at DESC,m.id DESC`
	out, e := r.readMessages(ctx, tx, where, []any{ids, actor}, actor)
	if e != nil {
		return nil, e
	}
	if len(out) != len(ids) {
		return nil, messager.ErrSyncForbidden
	}
	if e = r.decorate(ctx, tx, actor, out); e != nil {
		return nil, e
	}
	return out, nil
}
