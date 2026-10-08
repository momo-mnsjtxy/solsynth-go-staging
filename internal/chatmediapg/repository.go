// SPDX-License-Identifier: AGPL-3.0-only
package chatmediapg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"sort"
	"strconv"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db     *sql.DB
	access rooms.AccessPolicy
	none   int32
}

func New(db *sql.DB, access rooms.AccessPolicy, none int32) *Repository {
	return &Repository{db, access, none}
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.access == nil {
		return rooms.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT message_id,file_id,position,owner_authorized FROM owned_chat_media LIMIT 0`)
	return e
}

const fileColumns = `f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage`

func scanFile(row interface{ Scan(...any) error }) (v media.File, e error) {
	e = row.Scan(&v.ID, &v.AccountID, &v.Name, &v.MIMEType, &v.Size, &v.SHA256, &v.Public, &v.State, &v.CreatedAt, &v.UpdatedAt, &v.Usage)
	return
}
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor string, m *messager.MessageProjection, ids *[]string) error {
	if r == nil || r.access == nil || tx == nil || m == nil {
		return rooms.ErrUnavailable
	}
	if ids == nil {
		return nil
	}
	if !rooms.ValidID(actor) || !rooms.ValidID(m.ID) || len(*ids) > 32 {
		return rooms.ErrInvalid
	}
	ordered := append([]string{}, (*ids)...)
	sort.Strings(ordered)
	grants := map[string]bool{}
	for i, id := range ordered {
		if !rooms.ValidID(id) || i > 0 && ordered[i-1] == id {
			return rooms.ErrInvalid
		}
		f, e := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileColumns+` FROM owned_media_files f WHERE f.id=$1 FOR SHARE OF f`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return rooms.ErrNotFound
		}
		if e != nil {
			return e
		}
		var active bool
		e = tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1`, f.AccountID).Scan(&active)
		if e != nil {
			return e
		}
		if !active || f.State != "ready" {
			return rooms.ErrNotFound
		}
		// A reader cannot redistribute somebody else's private file to another room.
		if f.AccountID != actor && !f.Public {
			return rooms.ErrForbidden
		}
		grants[id] = f.AccountID == actor
	}
	if _, e := tx.ExecContext(ctx, `DELETE FROM owned_chat_media WHERE message_id=$1`, m.ID); e != nil {
		return e
	}
	for i, id := range *ids {
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_chat_media(message_id,file_id,position,owner_authorized)VALUES($1,$2,$3,$4)`, m.ID, id, i, grants[id]); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) Remove(ctx context.Context, tx *sql.Tx, mid string) error {
	_, e := tx.ExecContext(ctx, `DELETE FROM owned_chat_media WHERE message_id=$1`, mid)
	return e
}
func (r *Repository) roomPredicate() string {
	return `c.deleted_at IS NULL AND c.owned_owner_id IS NOT NULL AND c.encryption_mode=` + strconv.FormatInt(int64(r.none), 10) + ` AND ` + r.access.ReadSQL("c.id", "$1") + ` AND (c.is_public OR EXISTS(SELECT 1 FROM chat_members viewer JOIN owned_authority_accounts a ON a.id=viewer.account_id WHERE viewer.chat_room_id=c.id AND viewer.account_id=NULLIF($1,'')::uuid AND viewer.joined_at IS NOT NULL AND viewer.leave_at IS NULL AND viewer.deleted_at IS NULL AND NOT viewer.owned_banned AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))))`
}

const activeFile = `f.state='ready' AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=owner.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))`

func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, m *messager.MessageProjection) error {
	if r == nil || r.access == nil || tx == nil || m == nil {
		return rooms.ErrUnavailable
	}
	m.Attachments = json.RawMessage(`[]`)
	if m.DeletedAt != nil {
		return nil
	}
	mid := m.ID
	if m.Type == "messages.update" {
		var meta struct {
			ID string `json:"message_id"`
		}
		if json.Unmarshal(m.Meta, &meta) != nil || !rooms.ValidID(meta.ID) {
			return nil
		}
		mid = meta.ID
	} else if m.Type != "text" {
		return nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fileColumns+` FROM owned_chat_media l JOIN chat_messages m ON m.id=l.message_id JOIN chat_rooms c ON c.id=m.chat_room_id JOIN owned_media_files f ON f.id=l.file_id JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE l.message_id=$2 AND m.chat_room_id=$3 AND m.deleted_at IS NULL AND `+r.roomPredicate()+` AND `+activeFile+` AND (f.is_public OR f.account_id::text=$1 OR l.owner_authorized) ORDER BY l.position`, actor, mid, m.ChatRoomID)
	if e != nil {
		return e
	}
	defer rows.Close()
	files := []map[string]any{}
	for rows.Next() {
		f, e := scanFile(rows)
		if e != nil {
			return e
		}
		files = append(files, media.LegacyFile(f))
	}
	if e = rows.Err(); e != nil {
		return e
	}
	m.Attachments, e = json.Marshal(files)
	return e
}

// Pure read in the canonical media transaction. Fresh room membership, social
// policy, file state and ownership determine both metadata and byte access.
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, fileID string) (bool, error) {
	if r == nil || r.access == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if !rooms.ValidID(fileID) || actor != "" && !rooms.ValidID(actor) {
		return false, nil
	}
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_media l JOIN chat_messages m ON m.id=l.message_id JOIN chat_rooms c ON c.id=m.chat_room_id JOIN owned_media_files f ON f.id=l.file_id JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE l.file_id=$2 AND l.owner_authorized AND m.deleted_at IS NULL AND `+activeFile+` AND `+r.roomPredicate()+`)`, actor, fileID).Scan(&allowed)
	return allowed, e
}
