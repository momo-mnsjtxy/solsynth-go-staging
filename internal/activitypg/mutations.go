// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/passport/activities"
)

func (r *Repository) Set(ctx context.Context, actor string, q activities.Request, session bool, id string) (out activities.Activity, e error) {
	if e = activities.Normalize(&q, session); e != nil {
		return
	}
	if actor == "" {
		return out, activities.ErrInvalid
	}
	permission := ""
	if id != "" {
		permission = activities.ManagePermission
	}
	tx, _, p, e := r.begin(ctx, actor, actor, permission)
	if e != nil {
		return
	}
	defer tx.Rollback()
	now, e := clock(ctx, tx)
	if e != nil {
		return
	}
	var v row
	exists := false
	if id != "" {
		v, e = scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, id, actor))
	} else if q.ManualID != nil {
		v, e = scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE account_id=$1::uuid AND manual_id=$2::text AND lease_expires_at>clock_timestamp() AND ended_at IS NULL AND deleted_at IS NULL ORDER BY created_at DESC,id LIMIT 1 FOR UPDATE`, actor, *q.ManualID))
	} else {
		e = sql.ErrNoRows
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return
	}
	if e == nil {
		exists = true
	}
	if id != "" && !exists {
		return out, activities.ErrNotFound
	}
	e = nil
	if exists {
		if v.EndedAt != nil || v.StartedAt != nil && (session == false && id == "" || !now.Before(v.LeaseExpiresAt)) {
			return out, activities.ErrConflict
		}
		eligible, x := r.eligible(ctx, tx, v)
		if x != nil {
			return out, x
		}
		if !eligible {
			if id != "" {
				return out, activities.ErrNotFound
			}
			exists = false
		}
	}
	original := v
	if !exists {
		if q.Type == nil {
			return out, activities.ErrInvalid
		}
		v = row{Activity: activities.Activity{AccountID: actor, ManualID: q.ManualID, Visibility: activities.Public, CreatedAt: now}, revision: 1}
		v.ID, e = uuid()
		if e != nil {
			return
		}
		if session {
			v.StartedAt = &now
		}
	} else {
		v.revision++
	}
	if id != "" {
		applyPatch(&v, q)
	} else {
		applyManual(&v, q, exists)
	}
	if v.Type == nil {
		return out, activities.ErrInvalid
	}
	activities.NormalizeContent(&v.Content)
	if session {
		if exists && v.StartedAt == nil {
			return out, activities.ErrConflict
		}
		if q.CatalogKey != nil {
			q.Visibility = v.Visibility
			catalog, x := r.findOrCreateCatalog(ctx, tx, actor, q, now)
			if x != nil {
				return out, x
			}
			if v.CatalogID != nil && *v.CatalogID != catalog {
				return out, activities.ErrConflict
			}
			v.CatalogID = &catalog
		}
	}
	// Renaming an active manual key must never create two live owners of that key.
	if v.ManualID != nil {
		var duplicate bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_presence_activities WHERE account_id=$1::uuid AND manual_id=$2::text AND id<>$3::uuid AND deleted_at IS NULL AND ended_at IS NULL AND lease_expires_at>clock_timestamp())`, actor, *v.ManualID, v.ID).Scan(&duplicate)
		if e != nil {
			return
		}
		if duplicate {
			return out, activities.ErrConflict
		}
	}
	if e = artwork(ctx, tx, actor, &v.Content, true); e != nil {
		return
	}
	now, e = clock(ctx, tx)
	if e != nil {
		return
	}
	v.UpdatedAt = now
	v.LeaseMinutes = q.LeaseMinutes
	v.LeaseExpiresAt = now.Add(time.Duration(q.LeaseMinutes) * time.Minute)
	if p.Session.ExpiresAt.Before(v.LeaseExpiresAt) {
		v.LeaseExpiresAt = p.Session.ExpiresAt
	}
	v.session = p.Session.ID
	v.version = p.Account.Version
	v.epoch = p.Session.AuthorizationEpoch
	if e = save(ctx, tx, v, exists); e != nil {
		return
	}
	action := "set"
	if session {
		action = "start"
	}
	if id != "" {
		action = "update"
	}
	if e = r.event(ctx, tx, v, action); e != nil {
		return
	}
	if _, e = r.check(ctx, tx, actor, permission); e != nil {
		return
	}
	if e = artwork(ctx, tx, actor, &v.Content, true); e != nil {
		return
	}
	if exists {
		eligible, x := r.eligible(ctx, tx, original)
		if x != nil {
			return out, x
		}
		if !eligible {
			return out, activities.ErrNotFound
		}
	}
	now, e = clock(ctx, tx)
	if e != nil {
		return
	}
	if !now.Before(v.LeaseExpiresAt) {
		return out, activities.ErrNotFound
	}
	if exists && (id == "" || original.StartedAt != nil) && !now.Before(original.LeaseExpiresAt) {
		return out, activities.ErrNotFound
	}
	if _, e = r.check(ctx, tx, actor, permission); e != nil {
		return
	}
	if e = tx.Commit(); e == nil {
		out = v.Activity
	}
	return
}
func applyManual(v *row, q activities.Request, exists bool) {
	// Both manual-ID entry points may renew content without repeating its type.
	if q.Type == nil {
		q.Type = v.Type
	}
	v.Content = q.Content
	v.ManualID = q.ManualID
	if !exists || q.VisibilitySpecified {
		v.Visibility = q.Visibility
	}
}
func applyPatch(v *row, q activities.Request) {
	if q.Type != nil {
		v.Type = q.Type
	}
	if q.ManualID != nil {
		v.ManualID = q.ManualID
	}
	if q.Provider != nil {
		v.Provider = q.Provider
	}
	if q.ReferenceID != nil {
		v.ReferenceID = q.ReferenceID
	}
	if q.Title != nil {
		v.Title = q.Title
	}
	if q.Subtitle != nil {
		v.Subtitle = q.Subtitle
	}
	if q.Caption != nil {
		v.Caption = q.Caption
	}
	if q.LargeImage != nil {
		v.LargeImage = q.LargeImage
	}
	if q.SmallImage != nil {
		v.SmallImage = q.SmallImage
	}
	if q.QueryableTerms != nil {
		v.QueryableTerms = q.QueryableTerms
	}
	if q.Tags != nil {
		v.Tags = q.Tags
	}
	if len(q.Meta) > 0 && string(q.Meta) != "null" {
		v.Meta = q.Meta
	}
}
func save(ctx context.Context, tx *sql.Tx, v row, exists bool) error {
	if exists {
		_, e := tx.ExecContext(ctx, `UPDATE owned_presence_activities SET manual_id=$3::text,catalog_id=$4::uuid,document=$5::jsonb,visibility=$6::integer,session_id=$7::uuid,account_version=$8::bigint,session_epoch=$9::bigint,lease_minutes=$10::integer,lease_expires_at=$11::timestamptz,started_at=$12::timestamptz,ended_at=$13::timestamptz,updated_at=$14::timestamptz,deleted_at=$15::timestamptz,revision=$16::bigint WHERE id=$1::uuid AND account_id=$2::uuid`, v.ID, v.AccountID, v.ManualID, v.CatalogID, encoded(v.Content), v.Visibility, v.session, v.version, v.epoch, v.LeaseMinutes, v.LeaseExpiresAt, v.StartedAt, v.EndedAt, v.UpdatedAt, v.DeletedAt, v.revision)
		return e
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_presence_activities(id,account_id,manual_id,catalog_id,document,visibility,session_id,account_version,session_epoch,lease_minutes,lease_expires_at,started_at,ended_at,created_at,updated_at,revision)VALUES($1::uuid,$2::uuid,$3::text,$4::uuid,$5::jsonb,$6::integer,$7::uuid,$8::bigint,$9::bigint,$10::integer,$11::timestamptz,$12::timestamptz,$13::timestamptz,$14::timestamptz,$15::timestamptz,$16::bigint)`, v.ID, v.AccountID, v.ManualID, v.CatalogID, encoded(v.Content), v.Visibility, v.session, v.version, v.epoch, v.LeaseMinutes, v.LeaseExpiresAt, v.StartedAt, v.EndedAt, v.CreatedAt, v.UpdatedAt, v.revision)
	return e
}
func (r *Repository) findOrCreateCatalog(ctx context.Context, tx *sql.Tx, actor string, q activities.Request, now time.Time) (id string, e error) {
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_activity_catalog WHERE account_id=$1::uuid AND provider=$2::text AND catalog_key=$3::text AND deleted_at IS NULL FOR UPDATE`, actor, value(q.Provider), *q.CatalogKey).Scan(&id)
	if e == nil {
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return
	}
	id, e = uuid()
	if e != nil {
		return
	}
	name := q.CatalogName
	if name == nil {
		name = q.Title
	}
	activities.NormalizeContent(&q.Content)
	if string(q.Meta) == "null" {
		q.Meta = []byte(`{}`)
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_activity_catalog(id,account_id,provider,catalog_key,name,document,visibility,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::text,$4::text,$5::text,$6::jsonb,$7::integer,$8::timestamptz,$8::timestamptz)`, id, actor, value(q.Provider), *q.CatalogKey, name, encoded(q.Content), q.Visibility, now)
	return
}
func (r *Repository) End(ctx context.Context, actor, manual string) (out activities.Activity, e error) {
	if actor == "" || strings.TrimSpace(manual) == "" || len(manual) > 4096 {
		return out, activities.ErrInvalid
	}
	tx, _, _, e := r.begin(ctx, actor, actor, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	v, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE account_id=$1::uuid AND manual_id=$2::text AND ended_at IS NULL AND started_at IS NOT NULL AND deleted_at IS NULL AND lease_expires_at>clock_timestamp() ORDER BY created_at DESC,id LIMIT 1 FOR UPDATE`, actor, manual))
	if errors.Is(e, sql.ErrNoRows) {
		return out, activities.ErrNotFound
	}
	if e != nil {
		return
	}
	eligible, e := r.eligible(ctx, tx, v)
	if e != nil {
		return
	}
	if !eligible {
		return out, activities.ErrNotFound
	}
	originalLease := v.LeaseExpiresAt
	now, e := clock(ctx, tx)
	if e != nil {
		return
	}
	v.EndedAt = &now
	v.LeaseExpiresAt = now
	v.UpdatedAt = now
	v.revision++
	if v.CatalogID != nil {
		if e = accumulate(ctx, tx, v, now); e != nil {
			return
		}
	}
	if e = save(ctx, tx, v, true); e != nil {
		return
	}
	if e = r.event(ctx, tx, v, "end"); e != nil {
		return
	}
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return
	}
	if e = artwork(ctx, tx, actor, &v.Content, false); e != nil {
		return
	}
	if e = artwork(ctx, tx, actor, &v.Content, false); e != nil {
		return
	}
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return
	}
	eligible, e = r.eligible(ctx, tx, v)
	if e != nil {
		return
	}
	if !eligible {
		return out, activities.ErrNotFound
	}
	now, e = clock(ctx, tx)
	if e != nil {
		return
	}
	if !now.Before(originalLease) {
		return out, activities.ErrNotFound
	}
	if e = tx.Commit(); e == nil {
		out = v.Activity
	}
	return
}
func accumulate(ctx context.Context, tx *sql.Tx, v row, now time.Time) error {
	c, e := scanCatalog(tx.QueryRowContext(ctx, `SELECT `+catalogFields+` FROM owned_activity_catalog WHERE id=$1::uuid AND account_id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, *v.CatalogID, v.AccountID))
	if e != nil {
		return e
	}
	seconds := int64(now.Sub(*v.StartedAt) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	seen := map[string]bool{}
	for _, t := range c.Tags {
		seen[t.Slug] = true
	}
	for _, t := range v.Tags {
		if !seen[t.Slug] {
			c.Tags = append(c.Tags, t)
			seen[t.Slug] = true
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_activity_catalog SET total_seconds=total_seconds+$3::bigint,session_count=session_count+1,last_active_at=$4::timestamptz,updated_at=$4::timestamptz,document=$5::jsonb WHERE id=$1::uuid AND account_id=$2::uuid`, c.ID, v.AccountID, seconds, now, encoded(c.Content))
	return e
}
func (r *Repository) Delete(ctx context.Context, actor, id, manual string) (e error) {
	if actor == "" || len(manual) > 4096 || manual == "" && !validID(id) {
		return activities.ErrInvalid
	}
	tx, _, _, e := r.begin(ctx, actor, actor, activities.ManagePermission)
	if e != nil {
		return
	}
	defer tx.Rollback()
	var v row
	if manual != "" {
		v, e = scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE account_id=$1::uuid AND manual_id=$2::text AND deleted_at IS NULL AND lease_expires_at>clock_timestamp() ORDER BY created_at DESC,id LIMIT 1 FOR UPDATE`, actor, manual))
	} else {
		v, e = scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE account_id=$1::uuid AND id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, actor, id))
	}
	if errors.Is(e, sql.ErrNoRows) {
		return activities.ErrNotFound
	}
	if e != nil {
		return
	}
	now, e := clock(ctx, tx)
	if e != nil {
		return
	}
	if !now.Before(v.LeaseExpiresAt) {
		v.DeletedAt = &now
	} else {
		v.LeaseExpiresAt = now
	}
	v.UpdatedAt = now
	v.revision++
	if e = save(ctx, tx, v, true); e != nil {
		return
	}
	if e = r.event(ctx, tx, v, "delete"); e != nil {
		return
	}
	if _, e = r.check(ctx, tx, actor, activities.ManagePermission); e != nil {
		return
	}
	return tx.Commit()
}
