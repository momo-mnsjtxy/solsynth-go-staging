// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed translation of licensed DysonNetwork CustomAppService at
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c, modified 2026-10-07.
package developpg

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"solsynth.local/go-migration/ports/develop"
)

var (
	ErrUnverifiedPolicy = errors.New("developpg: production status mapping and global filters unverified; repository disabled")
	ErrDatabaseRequired = errors.New("developpg: database required")
)

// Repository is a read-only discovery adapter. It never creates tables or owns
// the database connection. Shared global filters and enum values remain unknown.
type Repository struct {
	db               *sql.DB
	syntheticPolicy  bool
	productionStatus int32
}

// New remains disabled, including before any connection is opened or query sent.
func New(db *sql.DB) *Repository { return &Repository{db: db} }

// NewSyntheticCompatibilityRepository enables an isolated candidate policy:
// deleted_at IS NULL on apps/products/widgets and an explicitly supplied
// SYNTHETIC production status number. No value is an inferred upstream mapping.
// Disabled widgets are counted, as the source count adds no is_enabled filter.
func NewSyntheticCompatibilityRepository(db *sql.DB, productionStatus int32) *Repository {
	return &Repository{db: db, syntheticPolicy: true, productionStatus: productionStatus}
}

var _ develop.Reader = (*Repository)(nil)

const discoverWhere = `a.deleted_at IS NULL AND a.status = $1
 AND ($2::text IS NULL OR a.slug ILIKE $2 OR a.name ILIKE $2 OR a.description ILIKE $2)`

// Discover uses a read-only repeatable-read transaction for total and page. This
// deliberately stronger snapshot is a compatibility candidate: upstream issues
// separate queries without an explicit transaction. Search preserves PostgreSQL
// ILIKE wildcard/backslash semantics rather than escaping the user's pattern.
func (r *Repository) Discover(ctx context.Context, query develop.Query) (develop.Page, error) {
	if r == nil || !r.syntheticPolicy {
		return develop.Page{}, ErrUnverifiedPolicy
	}
	if err := ctx.Err(); err != nil {
		return develop.Page{}, err
	}
	if err := develop.ValidateQuery(query); err != nil {
		return develop.Page{}, err
	}
	if r.db == nil {
		return develop.Page{}, ErrDatabaseRequired
	}
	var search any
	if query.Search != nil {
		trimmed := strings.TrimSpace(*query.Search)
		if trimmed != "" {
			search = "%" + trimmed + "%"
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return develop.Page{}, err
	}
	defer tx.Rollback()
	result := develop.Page{Apps: make([]develop.AppDiscovery, 0)}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM custom_apps a WHERE `+discoverWhere, r.productionStatus, search).Scan(&result.Total); err != nil {
		return develop.Page{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id::text, a.slug, a.name, a.description,
 (SELECT count(*) FROM app_products p WHERE p.app_id=a.id AND p.deleted_at IS NULL),
 (SELECT count(*) FROM board_widgets w WHERE w.app_id=a.id AND w.deleted_at IS NULL)
 FROM custom_apps a WHERE `+discoverWhere+`
 ORDER BY a.name, a.slug LIMIT $3 OFFSET $4`, r.productionStatus, search, query.Take, query.Offset)
	if err != nil {
		return develop.Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var app develop.AppDiscovery
		if err = rows.Scan(&app.ID, &app.Slug, &app.Title, &app.Description, &app.ProductsCount, &app.WidgetsCount); err != nil {
			return develop.Page{}, err
		}
		result.Apps = append(result.Apps, app)
	}
	if err = rows.Err(); err != nil {
		return develop.Page{}, err
	}
	if err = rows.Close(); err != nil {
		return develop.Page{}, err
	}
	if err = tx.Commit(); err != nil {
		return develop.Page{}, err
	}
	return result, nil
}
