// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/messager/rooms"
	"sort"
	"strings"
)

// RoomMedia shares the canonical room transaction. It never commits independently.
type RoomMedia interface {
	Apply(context.Context, *sql.Tx, string, *rooms.Room, rooms.RoomPatch) (map[string]any, error)
	Load(context.Context, *sql.Tx, string, *rooms.Room) error
	Ready(context.Context) error
}

func (r *Repository) WithRoomMedia(v RoomMedia) *Repository { r.roomMedia = v; return r }

// The caller retains canonical account/realm/room locks before files. Rechecking
// after a media wait also observes timed punishments and current realm policy.
func (r *Repository) RequireRoomCustomizationInTransaction(ctx context.Context, tx *sql.Tx, actor, rid string, attaching bool) error {
	v, e := r.room(ctx, tx, rid, false)
	if e != nil {
		return e
	}
	if _, e = requireMember(ctx, tx, rid, actor); e != nil {
		return e
	}
	if e = r.manage(ctx, tx, v, actor); e != nil {
		return e
	}
	if e = r.readable(ctx, tx, v, actor); e != nil {
		return e
	}
	if attaching {
		return r.RequireRoomMediaInTransaction(ctx, tx, actor, rid)
	}
	return nil
}

func (r *Repository) applyRoomMedia(ctx context.Context, tx *sql.Tx, actor string, v *rooms.Room, p rooms.RoomPatch, emit bool) error {
	if p.PictureID == nil && p.BackgroundID == nil && !p.RemovePicture && !p.RemoveBackground {
		return nil
	}
	if r.roomMedia == nil {
		return rooms.ErrUnavailable
	}
	changes, e := r.roomMedia.Apply(ctx, tx, actor, v, p)
	if e != nil || !emit || len(changes) == 0 {
		return e
	}
	m, e := requireMember(ctx, tx, v.ID, actor)
	if e != nil {
		return e
	}
	var nick string
	if e = tx.QueryRowContext(ctx, `SELECT nick FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&nick); e != nil {
		return e
	}
	if nick == "" {
		nick = "Someone"
	}
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	content := nick + " updated chat info (" + strings.Join(keys, ", ") + ")."
	meta, e := json.Marshal(map[string]any{"event": "chat_info_updated", "changes": changes})
	if e != nil {
		return e
	}
	event, e := appendMessage(ctx, tx, v.ID, m.ID, "system.chat.updated", &content, nil, nil, meta, nil, nil)
	if e == nil {
		v.UpdatedAt = event.CreatedAt
	}
	return e
}
