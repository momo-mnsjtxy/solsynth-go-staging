// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

func (r *Repository) Create(ctx context.Context, actor, selector, key string, p live.Patch) (live.Stream, error) {
	if e := live.ValidatePatch(p); e != nil {
		return live.Stream{}, e
	}
	if len(key) > 128 || !live.Text(key, 128) {
		return live.Stream{}, live.ErrInvalid
	}
	if e := r.configured(); e != nil {
		return live.Stream{}, e
	}
	tx, _, v, e := r.locked(ctx, actor, "", selector, "livestreams.create", true, false)
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	digest := hash(struct {
		Publisher string
		Patch     live.Patch
	}{v.PublisherID, p})
	if key != "" {
		var id string
		var saved []byte
		e = tx.QueryRowContext(ctx, `SELECT stream_id::text,request_hash FROM owned_livestream_create_keys WHERE actor_id=$1 AND key_hash=$2`, actor, hash(key)).Scan(&id, &saved)
		if e == nil {
			if !bytes.Equal(digest, saved) {
				return v, live.ErrConflict
			}
			v, e = get(ctx, tx, id, false, false)
			if e != nil {
				return v, e
			}
			if _, e = r.visible(ctx, tx, actor, &v); e != nil {
				return v, e
			}
			return v, r.commit(ctx, tx, actor, "livestreams.create")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return v, e
		}
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_livestreams WHERE publisher_id=$1 AND deleted_at IS NULL`, v.PublisherID).Scan(&count); e != nil {
		return v, e
	}
	if count >= r.cfg.MaxStreamsPerPublisher {
		return v, live.ErrConflict
	}
	if _, e = r.thumbnail(ctx, tx, actor, p.ThumbnailID); e != nil {
		return v, e
	}
	id, e := uid()
	if e != nil {
		return v, e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return v, e
	}
	typ, visibility := live.Regular, live.Public
	if p.Type != nil {
		typ = *p.Type
	}
	if p.Visibility != nil {
		visibility = *p.Visibility
	}
	meta, e := json.Marshal(p.Metadata)
	if e != nil {
		return v, live.ErrInvalid
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestreams(id,publisher_id,title,description,slug,type,visibility,room_name,thumbnail_id,metadata,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::timestamptz,$11::timestamptz)`, id, v.PublisherID, p.Title, p.Description, p.Slug, typ, visibility, roomName(id, 1), p.ThumbnailID, string(meta), at)
	if e != nil {
		return v, e
	}
	if key != "" {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_create_keys(actor_id,key_hash,request_hash,stream_id)VALUES($1,$2,$3,$4)`, actor, hash(key), digest, id); e != nil {
			return v, e
		}
	}
	if e = audit(ctx, tx, id, actor, "livestreams.create"); e != nil {
		return v, e
	}
	v, e = get(ctx, tx, id, false, false)
	if e != nil {
		return v, e
	}
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "livestreams.create")
}
func (r *Repository) Update(ctx context.Context, actor, id string, p live.Patch) (live.Stream, error) {
	if e := live.ValidatePatch(p); e != nil {
		return live.Stream{}, e
	}
	if p.ThumbnailID != nil {
		return live.Stream{}, live.ErrUnsupported
	}
	tx, _, v, e := r.locked(ctx, actor, id, "", "livestreams.update", true, false)
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	if v.OperationState != "ready" && v.OperationState != "active" {
		return v, live.ErrConflict
	}
	if v.Status == live.Active && p.Visibility != nil && *p.Visibility != v.Visibility {
		return v, live.ErrConflict
	}
	if p.Visibility != nil && *p.Visibility != v.Visibility {
		if pending, x := r.outputPending(ctx, tx, id); x != nil {
			return v, x
		} else if pending {
			return v, live.ErrConflict
		}
	}
	if p.Title != nil {
		v.Title = p.Title
	}
	if p.Description != nil {
		v.Description = p.Description
	}
	if p.Slug != nil {
		v.Slug = p.Slug
	}
	if p.Type != nil {
		v.Type = *p.Type
	}
	if p.Visibility != nil {
		v.Visibility = *p.Visibility
	}
	if p.Metadata != nil {
		v.Metadata = p.Metadata
	}
	meta, _ := json.Marshal(v.Metadata)
	_, e = tx.ExecContext(ctx, `UPDATE owned_livestreams SET title=$2,description=$3,slug=$4,type=$5,visibility=$6,metadata=$7::jsonb,updated_at=clock_timestamp() WHERE id=$1`, id, v.Title, v.Description, v.Slug, v.Type, v.Visibility, string(meta))
	if e != nil {
		return v, e
	}
	if e = audit(ctx, tx, id, actor, "livestreams.update"); e != nil {
		return v, e
	}
	v, e = get(ctx, tx, id, false, false)
	if e != nil {
		return v, e
	}
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "livestreams.update")
}
func (r *Repository) Thumbnail(ctx context.Context, actor, id string, file *string) (live.Stream, error) {
	if file != nil && !authority.ValidID(*file) {
		return live.Stream{}, live.ErrInvalid
	}
	tx, _, v, e := r.locked(ctx, actor, id, "", "livestreams.thumbnail", true, false)
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	if _, e = r.thumbnail(ctx, tx, actor, file); e != nil {
		return v, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestreams SET thumbnail_id=$2,updated_at=clock_timestamp() WHERE id=$1`, id, file); e != nil {
		return v, e
	}
	if e = audit(ctx, tx, id, actor, "livestreams.thumbnail"); e != nil {
		return v, e
	}
	v, e = get(ctx, tx, id, false, false)
	if e != nil {
		return v, e
	}
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	return v, r.commit(ctx, tx, actor, "livestreams.thumbnail")
}
