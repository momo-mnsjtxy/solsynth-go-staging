// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
)

func (r *Repository) realmPermission(ctx context.Context, tx *sql.Tx, actor, rid string, key social.RealmPermission) error {
	provider, ok := r.social.(social.PermissionProvider)
	if !ok {
		return rooms.ErrUnavailable
	}
	e := provider.RealmPermissionForWriteInTransaction(ctx, tx, actor, rid, key)
	if errors.Is(e, social.ErrForbidden) || errors.Is(e, social.ErrNotFound) {
		return rooms.ErrForbidden
	}
	return e
}
func (r *Repository) RequireRoomMediaInTransaction(ctx context.Context, tx *sql.Tx, actor, rid string) error {
	var realm string
	e := tx.QueryRowContext(ctx, `SELECT realm_id::text FROM owned_chat_realm_links WHERE room_id=$1`, rid).Scan(&realm)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	return r.realmPermission(ctx, tx, actor, realm, social.CanUploadMedia)
}
