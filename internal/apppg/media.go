// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
)

func (r *Repository) WithMedia(m apps.MediaStore) *Repository { r.media = m; return r }
func (r *Repository) loadAppMedia(ctx context.Context, tx *sql.Tx, viewer string, a *apps.App) error {
	if r.media == nil {
		return nil
	}
	var e error
	a.Picture, a.Background, e = r.media.Load(ctx, tx, viewer, "app", a.ID)
	return e
}
func (r *Repository) loadProductMedia(ctx context.Context, tx *sql.Tx, viewer string, p *apps.Product) error {
	// Media metadata never comes from the durable product/commerce document.
	p.Picture, p.Background = nil, nil
	if r.media == nil {
		return nil
	}
	var e error
	p.Picture, p.Background, e = r.media.Load(ctx, tx, viewer, "product", p.ID)
	return e
}
func (r *Repository) applyMedia(ctx context.Context, tx *sql.Tx, actor, kind, id string, picture, background *string) error {
	if r.media == nil {
		if picture != nil || background != nil {
			return apps.ErrProvider
		}
		return nil
	}
	return r.media.Apply(ctx, tx, actor, kind, id, picture, background)
}

// Public projections use a fresh statement for audience/metadata and never
// acquire locks after the media file lock. Private projections keep the proof
// and ancestry locks through response construction and recheck after all waits.
func (r *Repository) read(ctx context.Context, s *apps.Scope, fn func(*sql.Tx) error) error {
	if r == nil || r.db == nil {
		return apps.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	authorize := func() error {
		if s == nil {
			return nil
		}
		if r.guard == nil {
			return apps.ErrUnavailable
		}
		auth, ok := r.guard.(interface {
			Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
		})
		if !ok {
			return apps.ErrUnavailable
		}
		if _, e := auth.Authenticate(ctx, r.db, tx, s.Actor); e != nil {
			return e
		}
		_, e := scope(ctx, tx, *s, 0, true)
		return e
	}
	if e = authorize(); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	if e = authorize(); e != nil {
		return e
	}
	return tx.Commit()
}
