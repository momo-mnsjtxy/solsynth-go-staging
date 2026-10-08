// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/passport/board"
	"strings"
	"time"
)

//go:embed schema.sql
var Schema string

type TransactionGuard interface {
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}
type Repository struct {
	db    *sql.DB
	guard TransactionGuard
	cache board.Cache
}

// New enables public reads and app-authenticated operations. Account-owned
// mutations require the original account proof and a transaction guard.
func New(db *sql.DB) *Repository { return &Repository{db: db} }
func NewWithAuthority(db *sql.DB, g TransactionGuard) *Repository {
	return &Repository{db: db, guard: g}
}

// WithBoardCache invalidates the shared account projection only after commit.
func (r *Repository) WithBoardCache(cache board.Cache) *Repository { r.cache = cache; return r }

var _ apps.Repository = (*Repository)(nil)
var _ board.Catalog = (*Repository)(nil)

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type scanner interface{ Scan(...any) error }

func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return apps.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return apps.ErrConflict
	}
	return e
}
func id() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func (r *Repository) write(ctx context.Context, actor, permission string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil || r.guard == nil {
		return apps.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return apps.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
		return e
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT statement_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, now.UTC()); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}
func audit(ctx context.Context, tx *sql.Tx, actor, app, action, target string, now time.Time) error {
	var a any
	if app != "" {
		a = app
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_apps_audit(actor_id,app_id,action,target_id,occurred_at)VALUES($1,$2,$3,$4,$5)`, actor, a, action, target, now)
	return e
}
func developer(ctx context.Context, q queryer, actor, name string, role int, lock bool) (apps.Developer, error) {
	var d apps.Developer
	s := `SELECT d.id::text,d.publisher_id::text,p.name,d.created_at FROM owned_developers d JOIN owned_publishers p ON p.id=d.publisher_id JOIN owned_publisher_members m ON m.publisher_id=p.id JOIN owned_authority_accounts a ON a.id=m.account_id WHERE lower(p.name)=lower($1) AND p.deleted_at IS NULL AND p.type=0 AND m.account_id=$2 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=$3 AND NOT a.disabled`
	if lock {
		s += ` FOR UPDATE OF p,m`
	}
	e := q.QueryRowContext(ctx, s, name, actor, role).Scan(&d.ID, &d.PublisherID, &d.Name, &d.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return d, apps.ErrForbidden
	}
	return d, e
}
func scope(ctx context.Context, q queryer, s apps.Scope, role int, lock bool) (apps.Developer, error) {
	if !authority.ValidID(s.Actor) || !authority.ValidID(s.Project) || s.App != "" && !authority.ValidID(s.App) {
		return apps.Developer{}, apps.ErrInvalid
	}
	d, e := developer(ctx, q, s.Actor, s.Developer, role, lock)
	if e != nil {
		return d, e
	}
	var yes bool
	query := `SELECT true FROM owned_dev_projects WHERE id=$1 AND developer_id=$2 AND deleted_at IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	e = q.QueryRowContext(ctx, query, s.Project, d.ID).Scan(&yes)
	if e != nil {
		return d, mapped(e)
	}
	if s.App != "" {
		query = `SELECT true FROM owned_custom_apps WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL`
		if lock {
			query += ` FOR UPDATE`
		}
		e = q.QueryRowContext(ctx, query, s.App, s.Project).Scan(&yes)
	}
	return d, mapped(e)
}
func (r *Repository) Developers(ctx context.Context, actor, name string, enroll bool) ([]apps.Developer, error) {
	out := []apps.Developer{}
	if r == nil || r.db == nil {
		return nil, apps.ErrUnavailable
	}
	if enroll {
		e := r.write(ctx, actor, "developers.create", func(tx *sql.Tx, now time.Time) error {
			var pub string
			if e := tx.QueryRowContext(ctx, `SELECT p.id::text FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id WHERE lower(p.name)=lower($1) AND p.deleted_at IS NULL AND p.type=0 AND m.account_id=$2 AND m.role=200 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL FOR UPDATE OF p,m`, name, actor).Scan(&pub); e != nil {
				return apps.ErrForbidden
			}
			uid, e := id()
			if e != nil {
				return e
			}
			d := apps.Developer{ID: uid, PublisherID: pub, Name: name, CreatedAt: now}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_developers(id,publisher_id,created_at)VALUES($1,$2,$3)`, d.ID, pub, now); e != nil {
				return e
			}
			out = append(out, d)
			return audit(ctx, tx, actor, "", "developers.enroll", uid, now)
		})
		return out, e
	}
	query := `SELECT d.id::text,d.publisher_id::text,p.name,d.created_at FROM owned_developers d JOIN owned_publishers p ON p.id=d.publisher_id WHERE p.deleted_at IS NULL AND p.type=0`
	args := []any{}
	if name != "" {
		query += ` AND lower(p.name)=lower($1)`
		args = append(args, name)
	} else {
		if !authority.ValidID(actor) {
			return nil, apps.ErrForbidden
		}
		query += ` AND EXISTS(SELECT 1 FROM owned_publisher_members m WHERE m.publisher_id=p.id AND m.account_id=$1 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL)`
		args = append(args, actor)
	}
	rows, e := r.db.QueryContext(ctx, query+` ORDER BY p.name`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var d apps.Developer
		if e = rows.Scan(&d.ID, &d.PublisherID, &d.Name, &d.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func projectScan(row scanner) (apps.Project, error) {
	var p apps.Project
	e := row.Scan(&p.ID, &p.DeveloperID, &p.Slug, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	return p, mapped(e)
}

const projectColumns = `id::text,developer_id::text,slug,name,description,created_at,updated_at`

func (r *Repository) Projects(ctx context.Context, s apps.Scope, projectID string, p *apps.ProjectPatch, action string) ([]apps.Project, error) {
	out := []apps.Project{}
	if r == nil || r.db == nil {
		return nil, apps.ErrUnavailable
	}
	if projectID != "" && !authority.ValidID(projectID) {
		return nil, apps.ErrInvalid
	}
	if action == "read" {
		d, e := developer(ctx, r.db, s.Actor, s.Developer, 0, false)
		if e != nil {
			return nil, e
		}
		rows, e := r.db.QueryContext(ctx, `SELECT `+projectColumns+` FROM owned_dev_projects WHERE developer_id=$1 AND deleted_at IS NULL AND($2='' OR id::text=$2) ORDER BY name,id`, d.ID, projectID)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := projectScan(rows)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	if p != nil {
		if e := apps.ValidateProject(*p, action == "create"); e != nil {
			return nil, e
		}
	}
	e := r.write(ctx, s.Actor, "developers.projects."+action, func(tx *sql.Tx, now time.Time) error {
		role := 100
		if action == "create" {
			role = 50
		}
		d, e := developer(ctx, tx, s.Actor, s.Developer, role, true)
		if e != nil {
			return e
		}
		var v apps.Project
		if action == "create" {
			uid, e := id()
			if e != nil {
				return e
			}
			v = apps.Project{ID: uid, DeveloperID: d.ID, Slug: *p.Slug, Name: *p.Name, Description: p.Description, CreatedAt: now, UpdatedAt: now}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_dev_projects(id,developer_id,slug,name,description,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$6)`, v.ID, d.ID, v.Slug, v.Name, v.Description, now)
			if e != nil {
				return e
			}
		} else {
			v, e = projectScan(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM owned_dev_projects WHERE id=$1 AND developer_id=$2 AND deleted_at IS NULL FOR UPDATE`, projectID, d.ID))
			if e != nil {
				return e
			}
			if action == "delete" {
				if _, e = tx.ExecContext(ctx, `UPDATE owned_dev_projects SET deleted_at=$2,updated_at=$2 WHERE id=$1`, v.ID, now); e != nil {
					return e
				}
			} else {
				if p.Slug != nil {
					v.Slug = *p.Slug
				}
				if p.Name != nil {
					v.Name = *p.Name
				}
				if p.Description != nil {
					v.Description = p.Description
				}
				v.UpdatedAt = now
				if _, e = tx.ExecContext(ctx, `UPDATE owned_dev_projects SET slug=$2,name=$3,description=$4,updated_at=$5 WHERE id=$1`, v.ID, v.Slug, v.Name, v.Description, now); e != nil {
					return e
				}
			}
		}
		out = append(out, v)
		return audit(ctx, tx, s.Actor, "", "developers.projects."+action, v.ID, now)
	})
	return out, e
}

const appColumns = `a.id::text,a.project_id::text,d.publisher_id::text,a.slug,a.name,a.description,a.status,a.links,a.oauth_config,a.created_at,a.updated_at`
const appJoins = ` FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id `

// Organization developer/apps remain unsupported until their public projections
// have a canonical realm policy. Never publish a private realm identity here.
const activeApp = `a.deleted_at IS NULL AND p.deleted_at IS NULL AND pub.deleted_at IS NULL AND pub.type=0`

func appScan(row scanner) (apps.App, error) {
	var a apps.App
	var l, o []byte
	e := row.Scan(&a.ID, &a.ProjectID, &a.PublisherID, &a.Slug, &a.Name, &a.Description, &a.Status, &l, &o, &a.CreatedAt, &a.UpdatedAt)
	if e != nil {
		return a, mapped(e)
	}
	if len(l) > 0 {
		if e = json.Unmarshal(l, &a.Links); e != nil {
			return a, e
		}
	}
	if len(o) > 0 {
		e = json.Unmarshal(o, &a.OAuthConfig)
	}
	return a, e
}
func encoded(v any) string { b, _ := json.Marshal(v); return string(b) }
func (r *Repository) Apps(ctx context.Context, s apps.Scope, p *apps.AppPatch, action string) ([]apps.App, error) {
	out := []apps.App{}
	if r == nil || r.db == nil {
		return nil, apps.ErrUnavailable
	}
	if action == "read" {
		if _, e := scope(ctx, r.db, s, 0, false); e != nil {
			return nil, e
		}
		rows, e := r.db.QueryContext(ctx, `SELECT `+appColumns+appJoins+` WHERE `+activeApp+` AND a.project_id=$1 AND($2='' OR a.id::text=$2) ORDER BY a.name,a.id`, s.Project, s.App)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			a, e := appScan(rows)
			if e != nil {
				return nil, e
			}
			out = append(out, a)
		}
		return out, rows.Err()
	}
	if p != nil {
		if e := apps.ValidateApp(*p, action == "create"); e != nil {
			return nil, e
		}
	}
	e := r.write(ctx, s.Actor, "developers.apps."+action, func(tx *sql.Tx, now time.Time) error {
		d, e := scope(ctx, tx, s, 50, true)
		if e != nil {
			return e
		}
		var a apps.App
		if action == "create" {
			uid, e := id()
			if e != nil {
				return e
			}
			a = apps.App{ID: uid, ProjectID: s.Project, PublisherID: d.PublisherID, CreatedAt: now}
		} else {
			a, e = appScan(tx.QueryRowContext(ctx, `SELECT `+appColumns+appJoins+` WHERE a.id=$1 AND `+activeApp, s.App))
			if e != nil {
				return e
			}
		}
		if action == "delete" {
			if _, e = tx.ExecContext(ctx, `UPDATE owned_custom_apps SET deleted_at=$2,updated_at=$2 WHERE id=$1`, a.ID, now); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_app_secrets SET revoked_at=$2,updated_at=$2 WHERE app_id=$1 AND revoked_at IS NULL`, a.ID, now); e != nil {
				return e
			}
		} else {
			if p.Slug != nil {
				a.Slug = *p.Slug
			}
			if p.Name != nil {
				a.Name = *p.Name
			}
			if p.Description != nil {
				a.Description = p.Description
			}
			if p.Status != nil {
				a.Status = *p.Status
			}
			if p.Links != nil {
				a.Links = p.Links
			}
			if p.OAuthConfig != nil {
				a.OAuthConfig = p.OAuthConfig
			}
			a.UpdatedAt = now
			if action == "create" {
				_, e = tx.ExecContext(ctx, `INSERT INTO owned_custom_apps(id,project_id,slug,name,description,status,links,oauth_config,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$9)`, a.ID, a.ProjectID, a.Slug, a.Name, a.Description, a.Status, encoded(a.Links), encoded(a.OAuthConfig), now)
			} else {
				_, e = tx.ExecContext(ctx, `UPDATE owned_custom_apps SET slug=$2,name=$3,description=$4,status=$5,links=$6::jsonb,oauth_config=$7::jsonb,updated_at=$8 WHERE id=$1`, a.ID, a.Slug, a.Name, a.Description, a.Status, encoded(a.Links), encoded(a.OAuthConfig), now)
			}
			if e != nil {
				return e
			}
			if p.BoardWidgets != nil {
				if _, e = tx.ExecContext(ctx, `DELETE FROM owned_app_widgets WHERE app_id=$1`, a.ID); e != nil {
					return e
				}
				for _, w := range *p.BoardWidgets {
					if e = saveWidget(ctx, tx, a.ID, w, now); e != nil {
						return e
					}
				}
			}
		}
		out = append(out, a)
		return audit(ctx, tx, s.Actor, a.ID, "developers.apps."+action, a.ID, now)
	})
	return out, e
}
func (r *Repository) PublicApp(ctx context.Context, slug string) (apps.App, error) {
	if r == nil || r.db == nil {
		return apps.App{}, apps.ErrUnavailable
	}
	return appScan(r.db.QueryRowContext(ctx, `SELECT `+appColumns+appJoins+` WHERE lower(a.slug)=lower($1) AND a.status=1 AND `+activeApp, slug))
}
func (r *Repository) Discover(ctx context.Context, q develop.Query) (develop.Page, error) {
	out := develop.Page{Apps: []develop.AppDiscovery{}}
	if r == nil || r.db == nil {
		return out, apps.ErrUnavailable
	}
	if e := develop.ValidateQuery(q); e != nil {
		return out, e
	}
	search := ""
	if q.Search != nil {
		search = strings.TrimSpace(*q.Search)
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	where := ` WHERE ` + activeApp + ` AND a.status=1 AND($1='' OR a.slug ILIKE '%'||$1||'%' OR a.name ILIKE '%'||$1||'%' OR a.description ILIKE '%'||$1||'%')`
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+appJoins+where, search).Scan(&out.Total); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT a.id::text,a.slug,a.name,a.description,(SELECT count(*) FROM owned_app_products x WHERE x.app_id=a.id AND x.deleted_at IS NULL),(SELECT count(*) FROM owned_app_widgets x WHERE x.app_id=a.id)`+appJoins+where+` ORDER BY a.name,a.slug LIMIT $2 OFFSET $3`, search, q.Take, q.Offset)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var a develop.AppDiscovery
		if e = rows.Scan(&a.ID, &a.Slug, &a.Title, &a.Description, &a.ProductsCount, &a.WidgetsCount); e != nil {
			rows.Close()
			return out, e
		}
		out.Apps = append(out.Apps, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	return out, tx.Commit()
}
