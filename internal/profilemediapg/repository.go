// SPDX-License-Identifier: AGPL-3.0-only
package profilemediapg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"sort"
	"strings"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db     *sql.DB
	next   mediapg.ReadVisibility
	social *socialpg.Repository
}

func New(db *sql.DB, next mediapg.ReadVisibility) *Repository {
	return &Repository{db: db, next: next, social: socialpg.New(db)}
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return accounts.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT account_id,slot,file_id,attached_at FROM owned_account_profile_media LIMIT 0; SELECT account_id,related_id,status FROM owned_social_effective_relationships LIMIT 0`)
	if e != nil {
		return e
	}
	if next, ok := r.next.(interface{ Ready(context.Context) error }); ok {
		return next.Ready(ctx)
	}
	return nil
}
func imageType(value string) bool {
	switch value {
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
func (r *Repository) visible(ctx context.Context, tx *sql.Tx, viewer, owner string) (bool, error) {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid`, owner).Scan(&active)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil || !active {
		return false, e
	}
	if viewer != "" && viewer != owner {
		blocked, e := r.social.IsBlockedInTransaction(ctx, tx, viewer, owner)
		return !blocked, e
	}
	return true, nil
}

// Apply treats omitted references as retention and explicit JSON null as removal.
// Linking one's private file deliberately shares it through this public profile;
// it does not rewrite the canonical public flag or grant ownership to a viewer.
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor string, p accounts.ProfilePatch) error {
	if r == nil || r.db == nil || tx == nil {
		return accounts.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return accounts.ErrForbidden
	}
	changes := map[string]*string{}
	if p.PicturePresent || p.PictureID != nil {
		changes["picture"] = p.PictureID
	}
	if p.BackgroundPresent || p.BackgroundID != nil {
		changes["background"] = p.BackgroundID
	}
	if len(changes) == 0 {
		return nil
	}
	ids := map[string]bool{}
	for _, id := range changes {
		if id != nil {
			if !authority.ValidID(*id) {
				return accounts.ErrInvalid
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
		f, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files f WHERE f.id=$1::uuid FOR SHARE`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return accounts.ErrNotFound
		}
		if e != nil {
			return e
		}
		if f.AccountID != actor {
			return accounts.ErrForbidden
		}
		if f.State != "ready" {
			return accounts.ErrNotFound
		}
		if !imageType(f.MIMEType) {
			return accounts.ErrInvalid
		}
		active, e := r.visible(ctx, tx, actor, f.AccountID)
		if e != nil {
			return e
		}
		if !active {
			return accounts.ErrNotFound
		}
	}
	for _, slot := range []string{"picture", "background"} {
		id, present := changes[slot]
		if !present {
			continue
		}
		if id == nil {
			if _, e := tx.ExecContext(ctx, `DELETE FROM owned_account_profile_media WHERE account_id=$1::uuid AND slot=$2::text`, actor, slot); e != nil {
				return e
			}
			continue
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_account_profile_media(account_id,slot,file_id,attached_at)VALUES($1::uuid,$2::text,$3::uuid,statement_timestamp())ON CONFLICT(account_id,slot)DO UPDATE SET file_id=EXCLUDED.file_id,attached_at=EXCLUDED.attached_at`, actor, slot, *id); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, viewer string, a *accounts.Account) error {
	if r == nil || tx == nil || a == nil || !authority.ValidID(a.ID) || viewer != "" && !authority.ValidID(viewer) {
		return accounts.ErrUnavailable
	}
	visible, e := r.visible(ctx, tx, viewer, a.ID)
	if e != nil {
		return e
	}
	if !visible {
		return accounts.ErrNotFound
	}
	if a.Profile == nil {
		return nil
	}
	a.Profile.Picture = nil
	a.Profile.Background = nil
	rows, e := tx.QueryContext(ctx, `SELECT l.slot,`+fields+` FROM owned_account_profile_media l JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=l.account_id WHERE l.account_id=$1::uuid AND f.state='ready' ORDER BY l.slot`, a.ID)
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
		if !imageType(f.MIMEType) {
			continue
		}
		if slot == "picture" {
			a.Profile.Picture = media.LegacyFile(f)
		} else {
			a.Profile.Background = media.LegacyFile(f)
		}
	}
	return rows.Err()
}

// Media reads already verified the ready file and its current owner. This
// extension adds only an active owner-authorized profile audience, then keeps
// other resource grants through next. It takes no locks after the media lock.
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, viewer, id string) (bool, error) {
	if r == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if !authority.ValidID(id) || viewer != "" && !authority.ValidID(viewer) {
		return false, nil
	}
	var owner, mime string
	e := tx.QueryRowContext(ctx, `SELECT l.account_id::text,f.mime_type FROM owned_account_profile_media l JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=l.account_id JOIN owned_accounts_profiles p ON p.account_id=l.account_id WHERE l.file_id=$1::uuid AND f.state='ready' LIMIT 1`, id).Scan(&owner, &mime)
	if e == nil && imageType(mime) {
		allowed, err := r.visible(ctx, tx, viewer, owner)
		if err != nil || allowed {
			return allowed, err
		}
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	if r.next != nil {
		return r.next.CanReadInTransaction(ctx, tx, viewer, id)
	}
	return false, nil
}
