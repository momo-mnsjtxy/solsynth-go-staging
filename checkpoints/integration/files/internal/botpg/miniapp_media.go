// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/media"
	"sort"
	"strings"
)

//go:embed miniapp_media_schema.sql
var MiniAppMediaSchema string

const miniMediaFields = `f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage`
const miniMediaActiveOwner = `NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments pun WHERE pun.account_id=owner.id AND pun.kind='disable_account' AND(pun.starts_at IS NULL OR pun.starts_at<=statement_timestamp())AND(pun.expires_at IS NULL OR pun.expires_at>statement_timestamp()))`

func scanMiniMedia(row interface{ Scan(...any) error }) (f media.File, err error) {
	err = row.Scan(&f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage)
	return
}
func miniImageType(typ string) bool {
	switch typ {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp":
		return true
	}
	return false
}
func applyMiniMedia(ctx context.Context, tx *sql.Tx, actor, id string, p *bots.MiniPatch) error {
	if p == nil {
		return nil
	}
	slots := map[string]*string{"icon": p.IconID, "background": p.BackgroundID}
	ids := []string{}
	for _, slot := range []string{"icon", "background"} {
		if v := slots[slot]; v != nil && strings.TrimSpace(*v) != "" {
			ids = append(ids, strings.ToLower(*v))
		}
	}
	sort.Strings(ids)
	for _, fileID := range ids {
		f, err := scanMiniMedia(tx.QueryRowContext(ctx, `SELECT `+miniMediaFields+` FROM owned_media_files f WHERE f.id=$1 FOR SHARE`, fileID))
		if errors.Is(err, sql.ErrNoRows) {
			return bots.ErrNotFound
		}
		if err != nil {
			return err
		}
		if f.AccountID != actor {
			return bots.ErrForbidden
		}
		if f.State != "ready" {
			return bots.ErrNotFound
		}
		if !miniImageType(f.MIMEType) {
			return bots.ErrInvalid
		}
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT `+miniMediaActiveOwner+` FROM owned_authority_accounts owner WHERE owner.id=$1`, actor).Scan(&active); err != nil {
			return err
		}
		if !active {
			return bots.ErrForbidden
		}
	}
	for _, slot := range []string{"icon", "background"} {
		v := slots[slot]
		if v == nil {
			continue
		}
		if strings.TrimSpace(*v) == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM owned_miniapp_media WHERE miniapp_id=$1 AND slot=$2`, id, slot); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO owned_miniapp_media(miniapp_id,slot,file_id,assigned_by,attached_at)VALUES($1,$2,$3,$4,clock_timestamp())ON CONFLICT(miniapp_id,slot)DO UPDATE SET file_id=EXCLUDED.file_id,assigned_by=EXCLUDED.assigned_by,attached_at=EXCLUDED.attached_at`, id, slot, *v, actor); err != nil {
			return err
		}
	}
	return nil
}

func loadMiniMedia(ctx context.Context, tx *sql.Tx, m *bots.MiniApp) error {
	rows, err := tx.QueryContext(ctx, `SELECT l.slot,`+miniMediaFields+` FROM owned_miniapp_media l JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=l.assigned_by JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE l.miniapp_id=$1 AND f.state='ready' AND `+miniMediaActiveOwner+` ORDER BY l.slot`, m.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var slot string
		var f media.File
		if err = rows.Scan(&slot, &f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage); err != nil {
			return err
		}
		if !miniImageType(f.MIMEType) {
			continue
		}
		if slot == "icon" {
			m.Icon = media.LegacyFile(f)
		} else {
			m.Background = media.LegacyFile(f)
		}
	}
	return rows.Err()
}

// CanReadInTransaction grants only the current owner-authorized association's
// published audience. Private app/project metadata remains behind miniapp view
// authority; its uploader can already open their own private file. This policy
// is read-only and canonical media runs it again after object-store waits.
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, fileID string) (bool, error) {
	if r == nil || r.db == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if !authority.ValidID(fileID) || actor != "" && !authority.ValidID(actor) {
		return false, nil
	}
	var yes bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+miniPublicJoin+` AND EXISTS(SELECT 1 FROM owned_media_files f JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE f.id=$1 AND f.state='ready' AND `+miniMediaActiveOwner+` AND ((m.package_file_id=f.id AND m.package_owner_id=f.account_id AND m.package_fingerprint=f.sha256) OR EXISTS(SELECT 1 FROM owned_miniapp_media l WHERE l.miniapp_id=m.id AND l.file_id=f.id AND l.assigned_by=f.account_id AND f.mime_type IN('image/png','image/jpeg','image/gif','image/webp','image/avif','image/bmp')))))`, fileID).Scan(&yes)
	return yes, err
}
