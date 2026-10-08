// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/passport/board"
	"strings"
	"time"
)

func products(ctx context.Context, q queryer, app, ref string, public bool) ([]apps.Product, error) {
	out := []apps.Product{}
	rows, e := q.QueryContext(ctx, `SELECT document FROM owned_app_products WHERE app_id=$1 AND deleted_at IS NULL AND($2='' OR id::text=$2 OR identifier=$2) ORDER BY identifier,id`, app, ref)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var v apps.Product
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		if !public || v.State.IsEnabled {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
func (r *Repository) PublicProducts(ctx context.Context, slug, ref string) ([]apps.Product, error) {
	a, e := r.PublicApp(ctx, slug)
	if e != nil {
		return nil, e
	}
	return products(ctx, r.db, a.ID, ref, true)
}
func (r *Repository) Products(ctx context.Context, s apps.Scope, ref string, p *apps.ProductPatch, action string) ([]apps.Product, error) {
	if r == nil || r.db == nil {
		return nil, apps.ErrUnavailable
	}
	if action == "read" {
		if _, e := scope(ctx, r.db, s, 0, false); e != nil {
			return nil, e
		}
		return products(ctx, r.db, s.App, ref, false)
	}
	if p != nil {
		if e := apps.ValidateProduct(*p, action == "create"); e != nil {
			return nil, e
		}
	}
	out := []apps.Product{}
	e := r.write(ctx, s.Actor, "developers.apps.products."+action, func(tx *sql.Tx, now time.Time) error {
		if _, e := scope(ctx, tx, s, 50, true); e != nil {
			return e
		}
		v := apps.Product{AppID: s.App, Price: json.Number("0"), Recurrence: "none", State: apps.ProductState{IsEnabled: true, StockMode: "unlimited"}, CreatedAt: now}
		if action == "create" {
			uid, e := id()
			if e != nil {
				return e
			}
			v.ID = uid
		} else {
			vs, e := products(ctx, tx, s.App, ref, false)
			if e != nil {
				return e
			}
			if len(vs) != 1 {
				return apps.ErrNotFound
			}
			v = vs[0]
		}
		if action == "delete" {
			if _, e := tx.ExecContext(ctx, `UPDATE owned_app_products SET deleted_at=$2,updated_at=$2 WHERE id=$1`, v.ID, now); e != nil {
				return e
			}
		} else {
			if p.Identifier != nil {
				v.Identifier = *p.Identifier
			}
			if p.DisplayName != nil {
				v.DisplayName = *p.DisplayName
			}
			if p.Description != nil {
				v.Description = p.Description
			}
			if p.Currency != nil {
				v.Currency = *p.Currency
			}
			if p.Price != nil {
				v.Price = *p.Price
			}
			if p.Recurrence != nil {
				v.Recurrence = *p.Recurrence
			}
			if p.GroupIdentifier != nil {
				v.GroupIdentifier = p.GroupIdentifier
			}
			if p.Fulfillment != nil {
				v.Fulfillment = p.Fulfillment
			}
			if p.State != nil {
				if p.State.IsEnabled != nil {
					v.State.IsEnabled = *p.State.IsEnabled
				}
				if p.State.StockMode != nil {
					v.State.StockMode = *p.State.StockMode
				}
				if p.State.StockQuantity != nil {
					v.State.StockQuantity = p.State.StockQuantity
				}
			}
			if v.State.StockMode == "manual" && v.State.StockQuantity == nil {
				return apps.ErrInvalid
			}
			v.UpdatedAt = now
			if action == "create" {
				if _, e := tx.ExecContext(ctx, `INSERT INTO owned_app_products(id,app_id,identifier,document,created_at,updated_at)VALUES($1,$2,$3,$4::jsonb,$5,$5)`, v.ID, s.App, v.Identifier, encoded(v), now); e != nil {
					return e
				}
			} else {
				if _, e := tx.ExecContext(ctx, `UPDATE owned_app_products SET identifier=$2,document=$3::jsonb,updated_at=$4 WHERE id=$1`, v.ID, v.Identifier, encoded(v), now); e != nil {
					return e
				}
			}
		}
		out = append(out, v)
		return audit(ctx, tx, s.Actor, s.App, "developers.apps.products."+action, v.ID, now)
	})
	return out, e
}
func saveWidget(ctx context.Context, tx *sql.Tx, app string, w board.Manifest, now time.Time) error {
	if w.RendererType == "" {
		w.RendererType = "data"
	}
	if w.FieldTypes == nil {
		w.FieldTypes = []board.FieldType{}
	}
	if w.RequiredFields == nil {
		w.RequiredFields = []string{}
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_app_widgets(app_id,key,manifest,created_at,updated_at)VALUES($1,$2,$3::jsonb,$4,$4)`, app, w.Key, encoded(w), now)
	return e
}
func widgets(ctx context.Context, q queryer, app, key string) ([]board.Manifest, error) {
	out := []board.Manifest{}
	rows, e := q.QueryContext(ctx, `SELECT manifest FROM owned_app_widgets WHERE app_id=$1 AND($2='' OR lower(key)=lower($2)) ORDER BY key`, app, key)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var w board.Manifest
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &w); e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (r *Repository) Widgets(ctx context.Context, s apps.Scope, key string, w *board.Manifest, action string) ([]board.Manifest, error) {
	if r == nil || r.db == nil {
		return nil, apps.ErrUnavailable
	}
	if action == "read" {
		if _, e := scope(ctx, r.db, s, 50, false); e != nil {
			return nil, e
		}
		return widgets(ctx, r.db, s.App, key)
	}
	if w != nil {
		if e := apps.ValidateWidget(*w); e != nil {
			return nil, e
		}
	}
	out := []board.Manifest{}
	e := r.write(ctx, s.Actor, "developers.apps.update", func(tx *sql.Tx, now time.Time) error {
		if _, e := scope(ctx, tx, s, 50, true); e != nil {
			return e
		}
		if action == "create" {
			if e := saveWidget(ctx, tx, s.App, *w, now); e != nil {
				return e
			}
			out = append(out, *w)
		} else {
			old, e := widgets(ctx, tx, s.App, key)
			if e != nil {
				return e
			}
			if len(old) != 1 {
				return apps.ErrNotFound
			}
			if action == "delete" {
				_, e = tx.ExecContext(ctx, `DELETE FROM owned_app_widgets WHERE app_id=$1 AND key=$2`, s.App, old[0].Key)
			} else {
				w.Key = old[0].Key
				if w.RendererType == "" {
					w.RendererType = "data"
				}
				_, e = tx.ExecContext(ctx, `UPDATE owned_app_widgets SET manifest=$3::jsonb,updated_at=$4 WHERE app_id=$1 AND key=$2`, s.App, w.Key, encoded(w), now)
				out = append(out, *w)
			}
			if e != nil {
				return e
			}
		}
		return audit(ctx, tx, s.Actor, s.App, "developers.apps.widgets."+action, s.App, now)
	})
	return out, e
}
func boardCapable(a apps.App) bool {
	if a.Status != apps.Production || a.OAuthConfig == nil {
		return false
	}
	for _, s := range a.OAuthConfig.AllowedScopes {
		if s == apps.BoardScope {
			return true
		}
	}
	return false
}
func activeClientByID(ctx context.Context, q queryer, app string, lock bool) (apps.App, error) {
	if !authority.ValidID(app) {
		return apps.App{}, apps.ErrInvalid
	}
	query := `SELECT ` + appColumns + appJoins + ` WHERE a.id=$1 AND ` + activeApp
	a, e := appScan(q.QueryRowContext(ctx, query, app))
	if e != nil {
		return a, e
	}
	if lock {
		// Match management lock order: publisher, project, then app. Re-read after waits.
		var yes bool
		if e = q.QueryRowContext(ctx, `SELECT true FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, a.PublisherID).Scan(&yes); e != nil {
			return a, mapped(e)
		}
		if e = q.QueryRowContext(ctx, `SELECT true FROM owned_dev_projects WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, a.ProjectID).Scan(&yes); e != nil {
			return a, mapped(e)
		}
		a, e = appScan(q.QueryRowContext(ctx, query+` FOR SHARE OF a`, app))
		if e != nil {
			return a, e
		}
	}
	if a.Status != apps.Production {
		return a, apps.ErrForbidden
	}
	return a, nil
}
func activeByID(ctx context.Context, q queryer, app string, lock bool) (apps.App, error) {
	a, e := activeClientByID(ctx, q, app, lock)
	if e == nil && !boardCapable(a) {
		e = apps.ErrForbidden
	}
	return a, e
}
func (r *Repository) Manifests(ctx context.Context, app string) ([]board.Manifest, error) {
	if r == nil || r.db == nil {
		return nil, board.ErrDependency
	}
	if _, e := activeByID(ctx, r.db, app, false); e != nil {
		return nil, board.ErrAppNotFound
	}
	return widgets(ctx, r.db, app, "")
}
func (r *Repository) Lookup(ctx context.Context, app, key string) (*board.Widget, error) {
	if r == nil || r.db == nil {
		return nil, board.ErrDependency
	}
	if _, e := activeByID(ctx, r.db, app, false); e != nil {
		return nil, board.ErrAppNotFound
	}
	ws, e := widgets(ctx, r.db, app, key)
	if e != nil {
		return nil, e
	}
	if len(ws) != 1 {
		return nil, board.ErrAppNotFound
	}
	w := ws[0]
	return &board.Widget{Key: w.Key, Enabled: w.IsEnabled, AllowMultiple: w.AllowMultiple}, nil
}
func (r *Repository) Validate(ctx context.Context, app, key string, p board.Payload) (board.Payload, error) {
	if r == nil || r.db == nil {
		return nil, board.ErrDependency
	}
	if _, e := activeByID(ctx, r.db, app, false); e != nil {
		return nil, board.ErrAppNotFound
	}
	ws, e := widgets(ctx, r.db, app, key)
	if e != nil {
		return nil, e
	}
	if len(ws) != 1 {
		return nil, board.ErrAppNotFound
	}
	return board.ValidateManifestPayload(ctx, ws[0], p, board.DocumentedEnvelope{})
}
func scopeContains(scopes []string, want string) bool {
	for _, s := range scopes {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
