// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed app/product/bot image references on canonical media.
package developmediapg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"sort"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/media"
)

//go:embed schema.sql
var Schema string

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return apps.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT kind,resource_id,slot,file_id,uploader_id,attached_at FROM owned_developer_media LIMIT 0`)
	return e
}
func kindOK(k string) bool { return k == "app" || k == "product" || k == "bot" }
func imageType(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp":
		return true
	}
	return false
}

// Call after current actor, publisher/member, project, app and target locks.
// Files lock in sorted UUID order. The owner must revalidate the original proof
// and current publisher/owner after Apply, before committing any effect.
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor, kind, resource string, picture, background *string) error {
	if r == nil || tx == nil {
		return apps.ErrUnavailable
	}
	if !authority.ValidID(actor) || !authority.ValidID(resource) || !kindOK(kind) {
		return apps.ErrInvalid
	}
	ids := map[string]bool{}
	for _, id := range []*string{picture, background} {
		if id != nil {
			if !authority.ValidID(*id) {
				return apps.ErrInvalid
			}
			ids[strings.ToLower(*id)] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		var uploader, state, mime string
		e := tx.QueryRowContext(ctx, `SELECT account_id::text,state,mime_type FROM owned_media_files WHERE id=$1 FOR SHARE`, id).Scan(&uploader, &state, &mime)
		if errors.Is(e, sql.ErrNoRows) {
			return apps.ErrNotFound
		}
		if e != nil {
			return e
		}
		if uploader != actor {
			return apps.ErrForbidden
		}
		if state != "ready" {
			return apps.ErrNotFound
		}
		if !imageType(mime) {
			return apps.ErrInvalid
		}
	}
	for i, id := range []*string{picture, background} {
		if id == nil {
			continue
		}
		slot := []string{"picture", "background"}[i]
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_developer_media(kind,resource_id,slot,file_id,uploader_id,attached_at)VALUES($1,$2,$3,$4,$5,clock_timestamp())ON CONFLICT(kind,resource_id,slot)DO UPDATE SET file_id=EXCLUDED.file_id,uploader_id=EXCLUDED.uploader_id,attached_at=EXCLUDED.attached_at`, kind, resource, slot, *id, actor); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) Remove(ctx context.Context, tx *sql.Tx, kind, resource string) error {
	if r == nil || tx == nil {
		return apps.ErrUnavailable
	}
	if !kindOK(kind) || !authority.ValidID(resource) {
		return apps.ErrInvalid
	}
	_, e := tx.ExecContext(ctx, `DELETE FROM owned_developer_media WHERE kind=$1 AND resource_id=$2`, kind, resource)
	return e
}

const joins = ` FROM owned_developer_media link
 JOIN owned_media_files f ON f.id=link.file_id AND f.account_id=link.uploader_id
 JOIN owned_authority_accounts uploader ON uploader.id=f.account_id
 JOIN owned_custom_apps app ON app.id=CASE
  WHEN link.kind='app' THEN link.resource_id
  WHEN link.kind='product' THEN (SELECT product.app_id FROM owned_app_products product WHERE product.id=link.resource_id AND product.deleted_at IS NULL)
  WHEN link.kind='bot' THEN (SELECT bot.app_id FROM owned_bots bot WHERE bot.id=link.resource_id AND bot.deleted_at IS NULL) END
 JOIN owned_dev_projects project ON project.id=app.project_id
 JOIN owned_developers developer ON developer.id=project.developer_id
 JOIN owned_publishers publisher ON publisher.id=developer.publisher_id
 JOIN owned_authority_accounts owner ON owner.id=publisher.account_id `
const active = `f.state='ready' AND f.mime_type IN('image/png','image/jpeg','image/gif','image/webp','image/avif','image/bmp')
 AND NOT uploader.disabled AND NOT owner.disabled
 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id IN(uploader.id,owner.id) AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))
 AND app.deleted_at IS NULL AND project.deleted_at IS NULL AND publisher.deleted_at IS NULL AND publisher.type=0 AND publisher.realm_id IS NULL `
const audience = ` AND (
 EXISTS(SELECT 1 FROM owned_publisher_members member JOIN owned_authority_accounts viewer ON viewer.id=member.account_id WHERE member.publisher_id=publisher.id AND member.account_id=NULLIF($1,'')::uuid AND member.joined_at IS NOT NULL AND member.revoked_at IS NULL AND NOT viewer.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=viewer.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp())))
 OR (app.status=1 AND publisher.shadowbanned_at IS NULL
 AND (link.kind<>'product' OR EXISTS(SELECT 1 FROM owned_app_products product WHERE product.id=link.resource_id AND product.deleted_at IS NULL AND product.document->'state'->>'is_enabled'='true'))
 AND (link.kind<>'bot' OR EXISTS(SELECT 1 FROM owned_bots bot JOIN owned_authority_accounts account ON account.id=bot.account_id WHERE bot.id=link.resource_id AND bot.deleted_at IS NULL AND bot.is_active AND NOT account.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=account.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp())))))) `
const fields = `f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage`

// Load and byte authorization share exactly one current audience. Reads take no
// extra locks under the canonical media lock; links never make a file public.
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, viewer, kind, resource string) (picture, background map[string]any, e error) {
	if r == nil || tx == nil {
		return nil, nil, apps.ErrUnavailable
	}
	if !kindOK(kind) || !authority.ValidID(resource) || viewer != "" && !authority.ValidID(viewer) {
		return nil, nil, apps.ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, `SELECT link.slot,`+fields+joins+` WHERE `+active+audience+` AND link.kind=$2 AND link.resource_id=$3 ORDER BY link.slot`, viewer, kind, resource)
	if e != nil {
		return nil, nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var slot string
		var f media.File
		if e = rows.Scan(&slot, &f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage); e != nil {
			return nil, nil, e
		}
		if slot == "picture" {
			picture = media.LegacyFile(f)
		} else {
			background = media.LegacyFile(f)
		}
	}
	return picture, background, rows.Err()
}
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, viewer, id string) (bool, error) {
	if r == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if !authority.ValidID(id) || viewer != "" && !authority.ValidID(viewer) {
		return false, nil
	}
	var yes bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+joins+` WHERE `+active+audience+` AND link.file_id=$2)`, viewer, id).Scan(&yes)
	return yes, e
}

var _ apps.MediaStore = (*Repository)(nil)
