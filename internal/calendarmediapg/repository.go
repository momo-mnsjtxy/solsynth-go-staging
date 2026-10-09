// SPDX-License-Identifier: AGPL-3.0-only
package calendarmediapg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"sort"
	"strings"
)

//go:embed schema.sql
var Schema string

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return notabledays.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT event_id,slot,file_id,attached_at FROM owned_calendar_event_media LIMIT 0; SELECT id,account_id,visibility,deleted_at FROM owned_calendar_events LIMIT 0; SELECT id,account_id,state,charge,usage FROM owned_media_files LIMIT 0`)
	return e
}
func imageType(s string) bool {
	switch s {
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

// Apply keeps omitted slots, clears explicit null slots, and accepts only ready,
// canonically owned passive images. Uploads already accounted for their bytes;
// attaching never creates a second charge or transforms a read grant into ownership.
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor, event string, p notabledays.EventPatch) error {
	if r == nil || r.db == nil || tx == nil {
		return notabledays.ErrUnavailable
	}
	if !authority.ValidID(actor) || !authority.ValidID(event) {
		return notabledays.ErrInvalid
	}
	changes := map[string]*string{}
	if p.IconPresent || p.IconID != nil {
		changes["icon"] = p.IconID
	}
	if p.BackgroundPresent || p.BackgroundID != nil {
		changes["background"] = p.BackgroundID
	}
	if len(changes) == 0 {
		return nil
	}
	var owner string
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_calendar_events WHERE id=$1::uuid AND deleted_at IS NULL`, event).Scan(&owner); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return notabledays.ErrNotFound
		}
		return e
	}
	if owner != actor {
		return notabledays.ErrForbidden
	}
	ids := map[string]bool{}
	for _, id := range changes {
		if id != nil {
			if !authority.ValidID(strings.ToLower(*id)) {
				return notabledays.ErrInvalid
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
			return notabledays.ErrNotFound
		}
		if e != nil {
			return e
		}
		if f.AccountID != actor {
			return notabledays.ErrForbidden
		}
		if f.State != "ready" {
			return notabledays.ErrNotFound
		}
		if !imageType(f.MIMEType) {
			return notabledays.ErrInvalid
		}
	}
	// The caller revalidates its original authority proof after these file waits.
	for _, slot := range []string{"icon", "background"} {
		id, present := changes[slot]
		if !present {
			continue
		}
		if id == nil {
			if _, e := tx.ExecContext(ctx, `DELETE FROM owned_calendar_event_media WHERE event_id=$1::uuid AND slot=$2::text`, event, slot); e != nil {
				return e
			}
			continue
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_calendar_event_media(event_id,slot,file_id,attached_at)VALUES($1::uuid,$2::text,$3::uuid,statement_timestamp())ON CONFLICT(event_id,slot)DO UPDATE SET file_id=EXCLUDED.file_id,attached_at=EXCLUDED.attached_at`, event, slot, *id); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, viewer string, v *notabledays.Event) error {
	if r == nil || r.db == nil || tx == nil || v == nil {
		return notabledays.ErrUnavailable
	}
	v.Icon = nil
	v.Background = nil
	allowed, e := notabledaypg.CanReadEventInTransaction(ctx, tx, viewer, v.ID)
	if e != nil {
		return e
	}
	if !allowed {
		return notabledays.ErrNotFound
	}
	rows, e := tx.QueryContext(ctx, `SELECT l.slot,`+fields+` FROM owned_calendar_event_media l JOIN owned_calendar_events e ON e.id=l.event_id JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=e.account_id WHERE l.event_id=$1::uuid AND e.deleted_at IS NULL AND f.state='ready' ORDER BY l.slot`, v.ID)
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
		if slot == "icon" {
			v.Icon = media.LegacyFile(f)
		} else {
			v.Background = media.LegacyFile(f)
		}
	}
	return rows.Err()
}
func (r *Repository) Remove(ctx context.Context, tx *sql.Tx, id string) error {
	if r == nil || tx == nil {
		return notabledays.ErrUnavailable
	}
	_, e := tx.ExecContext(ctx, `DELETE FROM owned_calendar_event_media WHERE event_id=$1::uuid`, id)
	return e
}

// Media has already checked file readiness and owner state. No locks are taken
// after its canonical file lock; this policy never changes independent grants.
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, viewer, id string) (bool, error) {
	if r == nil || r.db == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if !authority.ValidID(id) || viewer != "" && !authority.ValidID(viewer) {
		return false, nil
	}
	// Reference membership and current audience must come from one statement:
	// collecting event IDs first would retain a detached grant across the next
	// statement's wait. Ownership/readiness are checked in the same predicate.
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+notabledaypg.ReadableEventsSQL+` AND EXISTS(SELECT 1 FROM owned_calendar_event_media l JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=e.account_id WHERE l.event_id=e.id AND f.id=$2::uuid AND f.state='ready' AND f.mime_type IN('image/png','image/jpeg','image/gif','image/webp','image/avif','image/bmp')))`, viewer, id).Scan(&allowed)
	return allowed, e
}

// Validate observes the complete projected attachment set in one fresh statement
// after any per-event adapter waits. No locks, stores, quotas or grants change.
func (r *Repository) Validate(ctx context.Context, tx *sql.Tx, events []notabledays.Event) error {
	if r == nil || r.db == nil || tx == nil {
		return notabledays.ErrUnavailable
	}
	if len(events) == 0 {
		return nil
	}
	ids := make([]string, 0, len(events))
	current := map[string][2]any{}
	for _, event := range events {
		ids = append(ids, event.ID)
		current[event.ID] = [2]any{}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT l.event_id::text,l.slot,`+fields+` FROM owned_calendar_event_media l JOIN owned_calendar_events e ON e.id=l.event_id JOIN owned_media_files f ON f.id=l.file_id AND f.account_id=e.account_id WHERE l.event_id IN(SELECT value::uuid FROM jsonb_array_elements_text($1::jsonb)) AND e.deleted_at IS NULL AND f.state='ready' ORDER BY l.event_id,l.slot`, string(encoded))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var event, slot string
		var f media.File
		if err = rows.Scan(&event, &slot, &f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage); err != nil {
			return err
		}
		if !imageType(f.MIMEType) {
			continue
		}
		value := current[event]
		index := 0
		if slot == "background" {
			index = 1
		}
		value[index] = media.LegacyFile(f)
		current[event] = value
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, event := range events {
		want, err := json.Marshal([2]any{event.Icon, event.Background})
		if err != nil {
			return err
		}
		got, err := json.Marshal(current[event.ID])
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			return notabledays.ErrConflict
		}
	}
	return nil
}
