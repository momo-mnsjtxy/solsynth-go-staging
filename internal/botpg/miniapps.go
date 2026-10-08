// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"time"
)

func loadMini(ctx context.Context, tx *sql.Tx, app, id string) (v bots.MiniApp, e error) {
	var raw []byte
	e = tx.QueryRowContext(ctx, `SELECT id::text,app_id::text,project_id::text,slug,plugin_id,stage,manifest,download_count,created_at,updated_at,deleted_at FROM owned_bot_miniapps WHERE id=$1 AND app_id=$2`, id, app).Scan(&v.ID, &v.AppID, &v.ProjectID, &v.Slug, &v.PluginID, &v.Stage, &raw, &v.DownloadCount, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if e != nil {
		return v, mapped(e)
	}
	v.Version = "1.0.0"
	if len(raw) > 0 {
		var m bots.MiniManifest
		if e = json.Unmarshal(raw, &m); e != nil {
			return v, e
		}
		v.Manifest = &m
		v.Name = &m.Name
		v.Version = m.Version
		v.Author = m.Author
		v.Description = m.Description
		v.EntryURL = m.EntryURL
		v.Homepage = m.Homepage
	}
	var sha string
	var size int64
	x := tx.QueryRowContext(ctx, `SELECT f.sha256,f.size FROM owned_bot_miniapps m JOIN owned_media_files f ON f.id=m.package_file_id JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE m.id=$1 AND f.state='ready' AND f.is_public AND NOT owner.disabled AND m.package_fingerprint=f.sha256`, id).Scan(&sha, &size)
	if x == nil {
		url := "/api/miniapps/" + v.Slug + "/package"
		v.PackageURL = &url
		v.PackageSHA256 = &sha
		v.PackageSize = &size
	} else if x != sql.ErrNoRows {
		return v, x
	}
	return v, nil
}
func (r *Repository) MiniApps(ctx context.Context, s apps.Scope, ref string, p *bots.MiniPatch, action string) (out []bots.MiniApp, e error) {
	out = []bots.MiniApp{}
	role := 0
	permission := "developers.miniapps.view"
	switch action {
	case "read":
		if !authority.ValidID(ref) {
			return out, bots.ErrInvalid
		}
	case "create", "update", "delete":
		role = 50
		permission = "developers.miniapps." + action
	default:
		return out, bots.ErrInvalid
	}
	if action == "create" && ref != "" || action != "create" && !authority.ValidID(ref) {
		return out, bots.ErrInvalid
	}
	if action == "create" || action == "update" {
		if p == nil {
			return out, bots.ErrInvalid
		}
		if e = bots.ValidateMini(*p, action == "create"); e != nil {
			return out, e
		}
	}
	e = r.within(ctx, s, "", permission, role, func(tx *sql.Tx, scope appScope, now time.Time) error {
		if action == "create" {
			if e := capacity(ctx, tx, scope.publisher, true, now); e != nil {
				return e
			}
			id, e := newID()
			if e != nil {
				return e
			}
			ref = id
			stage := 0
			if p.Stage != nil {
				stage = *p.Stage
			}
			if stage == 2 && p.Manifest == nil {
				return bots.ErrInvalid
			}
			var manifest any
			var plugin any
			if p.Manifest != nil {
				manifest = encoded(p.Manifest)
				plugin = p.Manifest.ID
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_bot_miniapps(id,app_id,project_id,slug,plugin_id,stage,manifest,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, id, s.App, s.Project, *p.Slug, plugin, stage, manifest, now); e != nil {
				return e
			}
		} else {
			old, e := loadMini(ctx, tx, s.App, ref)
			if e != nil {
				return e
			}
			if old.DeletedAt != nil {
				if action == "delete" {
					return nil
				}
				return bots.ErrNotFound
			}
			if action == "delete" {
				if _, e = tx.ExecContext(ctx, `UPDATE owned_bot_miniapps SET deleted_at=$2,updated_at=$2 WHERE id=$1`, ref, now); e != nil {
					return e
				}
				return audit(ctx, tx, s, "miniapp.delete", ref, now)
			}
			if action == "update" {
				stage := old.Stage
				if p.Stage != nil {
					stage = *p.Stage
				}
				manifest := old.Manifest
				if p.Manifest != nil {
					manifest = p.Manifest
				}
				if stage == 2 && manifest == nil {
					return bots.ErrInvalid
				}
				var raw any
				var plugin any
				if manifest != nil {
					raw = encoded(manifest)
					plugin = manifest.ID
				}
				if _, e = tx.ExecContext(ctx, `UPDATE owned_bot_miniapps SET slug=$2,plugin_id=$3,stage=$4,manifest=$5,updated_at=$6 WHERE id=$1`, ref, textOr(p.Slug, old.Slug), plugin, stage, raw, now); e != nil {
					return e
				}
			}
		}
		v, e := loadMini(ctx, tx, s.App, ref)
		if e != nil {
			return e
		}
		out = append(out, v)
		if action == "read" {
			return nil
		}
		return audit(ctx, tx, s, "miniapp."+action, ref, now)
	})
	return
}
func (r *Repository) ListMiniApps(ctx context.Context, s apps.Scope, offset, take int) (out []bots.MiniApp, total int64, e error) {
	out = []bots.MiniApp{}
	if offset < 0 || offset > 100000 || take < 1 || take > 100 {
		return out, 0, bots.ErrInvalid
	}
	e = r.within(ctx, s, "", "developers.miniapps.view", 0, func(tx *sql.Tx, _ appScope, _ time.Time) error {
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_bot_miniapps WHERE app_id=$1 AND deleted_at IS NULL`, s.App).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_bot_miniapps WHERE app_id=$1 AND deleted_at IS NULL ORDER BY created_at,id OFFSET $2 LIMIT $3`, s.App, offset, take)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			v, e := loadMini(ctx, tx, s.App, id)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	return
}

const miniPublicJoin = ` FROM owned_bot_miniapps m JOIN owned_custom_apps a ON a.id=m.app_id JOIN owned_dev_projects project ON project.id=a.project_id JOIN owned_developers d ON d.id=project.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id WHERE m.stage=2 AND m.deleted_at IS NULL AND ` + publicAncestry

func (r *Repository) DiscoverMiniApps(ctx context.Context, offset, take int, search string) (out []bots.MiniApp, total int64, e error) {
	out = []bots.MiniApp{}
	if offset < 0 || offset > 100000 || take < 1 || take > 100 || len(search) > 256 {
		return out, 0, bots.ErrInvalid
	}
	e = r.public(ctx, func(tx *sql.Tx, _ time.Time) error {
		where := miniPublicJoin + ` AND($1='' OR strpos(lower(COALESCE(m.manifest->>'name','')||' '||COALESCE(m.manifest->>'description','')||' '||COALESCE(m.plugin_id,'')),lower($1))>0)`
		if e := tx.QueryRowContext(ctx, `SELECT count(*)`+where, search).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT m.id::text,m.app_id::text`+where+` ORDER BY m.manifest->>'name',m.slug,m.id OFFSET $2 LIMIT $3`, search, offset, take)
		if e != nil {
			return e
		}
		type item struct{ id, app string }
		ids := []item{}
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.app); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, v := range ids {
			m, e := loadMini(ctx, tx, v.app, v.id)
			if e != nil {
				return e
			}
			d, e := developer(ctx, tx, v.app)
			if e != nil {
				return e
			}
			m.Developer = &d
			if d.Publisher != nil {
				author := d.Publisher.Nick
				if author == "" {
					author = d.Publisher.Name
				}
				m.Author = &author
			}
			out = append(out, m)
		}
		return nil
	})
	return
}
func (r *Repository) PublicMiniApp(ctx context.Context, slug string) (out bots.MiniApp, e error) {
	if len(slug) < 2 || len(slug) > 256 {
		return out, bots.ErrInvalid
	}
	e = r.public(ctx, func(tx *sql.Tx, _ time.Time) error {
		var id, app string
		if e := tx.QueryRowContext(ctx, `SELECT m.id::text,m.app_id::text`+miniPublicJoin+` AND lower(m.slug)=lower($1)`, slug).Scan(&id, &app); e != nil {
			return e
		}
		var e error
		out, e = loadMini(ctx, tx, app, id)
		if e != nil {
			return e
		}
		d, e := developer(ctx, tx, app)
		if e != nil {
			return e
		}
		out.Developer = &d
		if d.Publisher != nil {
			author := d.Publisher.Nick
			if author == "" {
				author = d.Publisher.Name
			}
			out.Author = &author
		}
		return nil
	})
	return
}
