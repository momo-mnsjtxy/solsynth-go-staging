// SPDX-License-Identifier: AGPL-3.0-only
// Canonical room image references; no copied bytes, ownership or quota ledger.
package roommediapg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager/rooms"
	"sort"
	"strconv"
)

//go:embed schema.sql
var Schema string

type Access interface {
	rooms.AccessPolicy
	RequireRoomCustomizationInTransaction(context.Context, *sql.Tx, string, string, bool) error
}
type Repository struct {
	db     *sql.DB
	access Access
	none   int32
}

func New(db *sql.DB, access Access, none int32) *Repository { return &Repository{db, access, none} }
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.access == nil {
		return rooms.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT room_id,slot,file_id,account_id,attached_at FROM owned_chat_room_media LIMIT 0`)
	return e
}
func imageType(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp":
		return true
	}
	return false
}

const fields = `f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage`

func scan(row interface{ Scan(...any) error }) (f media.File, e error) {
	e = row.Scan(&f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage)
	return
}

const liveFile = `f.state='ready' AND f.account_id=l.account_id AND f.mime_type IN('image/png','image/jpeg','image/gif','image/webp','image/avif','image/bmp') AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=owner.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))`

// Both the room audience and linked social policy must allow the same viewer.
// Encryption prevents a public-flag shortcut. Pending/removed/banned members do
// not gain access to a private room through its image IDs or generic file route.
func (r *Repository) audience() string {
	mode := strconv.FormatInt(int64(r.none), 10)
	return `c.deleted_at IS NULL AND c.owned_owner_id IS NOT NULL AND c.encryption_mode IN(` + mode + `,3) AND ` + r.access.ReadSQL("c.id", "$1") + ` AND ((c.is_public AND c.encryption_mode=` + mode + `) OR EXISTS(SELECT 1 FROM chat_members viewer JOIN owned_authority_accounts account ON account.id=viewer.account_id WHERE viewer.chat_room_id=c.id AND viewer.account_id=NULLIF($1,'')::uuid AND viewer.joined_at IS NOT NULL AND viewer.leave_at IS NULL AND viewer.deleted_at IS NULL AND NOT viewer.owned_banned AND NOT account.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=account.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))))`
}

// Omitted and null IDs retain the existing reference, matching the controller.
// The owned explicit DELETE endpoint sets RemovePicture/RemoveBackground.
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor string, room *rooms.Room, p rooms.RoomPatch) (map[string]any, error) {
	changes := map[string]any{}
	if r == nil || r.access == nil || tx == nil || room == nil {
		return nil, rooms.ErrUnavailable
	}
	if !rooms.ValidID(actor) || !rooms.ValidID(room.ID) {
		return nil, rooms.ErrInvalid
	}
	selections := map[string]*string{}
	if p.PictureID != nil || p.RemovePicture {
		selections["picture"] = p.PictureID
	}
	if p.BackgroundID != nil || p.RemoveBackground {
		selections["background"] = p.BackgroundID
	}
	if len(selections) == 0 {
		return changes, nil
	}
	attaching := p.PictureID != nil || p.BackgroundID != nil
	if e := r.access.RequireRoomCustomizationInTransaction(ctx, tx, actor, room.ID, attaching); e != nil {
		return nil, e
	}
	ids := map[string]bool{}
	for _, id := range selections {
		if id != nil {
			if !rooms.ValidID(*id) {
				return nil, rooms.ErrInvalid
			}
			ids[*id] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		f, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files f WHERE f.id=$1 FOR SHARE OF f`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return nil, rooms.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		// Visibility through any other resource, including public files, is never ownership.
		if f.AccountID != actor {
			return nil, rooms.ErrForbidden
		}
		if f.State != "ready" {
			return nil, rooms.ErrNotFound
		}
		if !imageType(f.MIMEType) {
			return nil, rooms.ErrInvalid
		}
		var active bool
		e = tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1`, actor).Scan(&active)
		if e != nil {
			return nil, e
		}
		if !active {
			return nil, rooms.ErrForbidden
		}
	}
	if e := r.access.RequireRoomCustomizationInTransaction(ctx, tx, actor, room.ID, attaching); e != nil {
		return nil, e
	}
	for _, slot := range []string{"picture", "background"} {
		selected, present := selections[slot]
		if !present {
			continue
		}
		var old sql.NullString
		e := tx.QueryRowContext(ctx, `SELECT file_id::text FROM owned_chat_room_media WHERE room_id=$1 AND slot=$2`, room.ID, slot).Scan(&old)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		if selected == nil && !old.Valid || selected != nil && old.Valid && *selected == old.String {
			continue
		}
		var prior any
		if old.Valid {
			prior = old.String
		}
		changes[slot+"_id"] = map[string]any{"old": prior, "new": selected}
		if selected == nil {
			_, e = tx.ExecContext(ctx, `DELETE FROM owned_chat_room_media WHERE room_id=$1 AND slot=$2`, room.ID, slot)
		} else {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_room_media(room_id,slot,file_id,account_id,attached_at)VALUES($1,$2,$3,$4,clock_timestamp())ON CONFLICT(room_id,slot)DO UPDATE SET file_id=EXCLUDED.file_id,account_id=EXCLUDED.account_id,attached_at=EXCLUDED.attached_at`, room.ID, slot, *selected, actor)
		}
		if e != nil {
			return nil, e
		}
	}
	return changes, nil
}
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, room *rooms.Room) error {
	if r == nil || r.access == nil || tx == nil || room == nil {
		return rooms.ErrUnavailable
	}
	if !rooms.ValidID(room.ID) || actor != "" && !rooms.ValidID(actor) {
		return rooms.ErrInvalid
	}
	room.Picture = nil
	room.Background = nil
	rows, e := tx.QueryContext(ctx, `SELECT l.slot,`+fields+` FROM owned_chat_room_media l JOIN chat_rooms c ON c.id=l.room_id JOIN owned_media_files f ON f.id=l.file_id JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE l.room_id=$2 AND `+liveFile+` AND `+r.audience()+` ORDER BY l.slot`, actor, room.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var slot string
		var f media.File
		if e = rows.Scan(&slot, &f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage); e != nil {
			return e
		}
		if slot == "picture" {
			room.Picture = media.LegacyFile(f)
		} else {
			room.Background = media.LegacyFile(f)
		}
	}
	return rows.Err()
}

// Called under the canonical media file lock; acquire no other locks here.
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	if r == nil || r.access == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if !rooms.ValidID(id) || actor != "" && !rooms.ValidID(actor) {
		return false, nil
	}
	var yes bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_room_media l JOIN chat_rooms c ON c.id=l.room_id JOIN owned_media_files f ON f.id=l.file_id JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE l.file_id=$2 AND `+liveFile+` AND `+r.audience()+`)`, actor, id).Scan(&yes)
	return yes, e
}
