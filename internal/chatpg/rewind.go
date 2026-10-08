// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/messager/rooms"
)

// RewindRoom joins a trusted report read and retains current room/relationship
// visibility. No public report may request a direct room through this seam.
func (r *Repository) RewindRoom(ctx context.Context, db *sql.DB, tx *sql.Tx, viewer, id string) (rooms.Room, error) {
	if r == nil || r.db != db || tx == nil {
		return rooms.Room{}, rooms.ErrUnavailable
	}
	v, e := r.room(ctx, tx, id, false)
	if e != nil {
		return v, e
	}
	if viewer == "" && v.Type != 0 {
		return rooms.Room{}, rooms.ErrForbidden
	}
	if e = r.readable(ctx, tx, v, viewer); e != nil {
		return rooms.Room{}, e
	}
	if e = r.decorateRoom(ctx, tx, &v, viewer); e != nil {
		return rooms.Room{}, e
	}
	return v, nil
}
