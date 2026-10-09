// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from DysonNetwork Develop at
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.
package developpg

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"solsynth.local/go-migration/ports/develop"
)

type PackageRepository struct {
	db              *sql.DB
	candidate       bool
	productionStage int32
}

func NewPackageRepository(db *sql.DB) *PackageRepository { return &PackageRepository{db: db} }

// NewSyntheticCompatibilityPackageRepository selects a caller-supplied fixture
// stage and deleted_at IS NULL on miniapps/projects, with required developer
// membership by relation existence. This is not verified shared global policy.
func NewSyntheticCompatibilityPackageRepository(db *sql.DB, productionStage int32) *PackageRepository {
	return &PackageRepository{db: db, candidate: true, productionStage: productionStage}
}

var _ develop.PackageRepository = (*PackageRepository)(nil)

func (r *PackageRepository) check(ctx context.Context) error {
	if r == nil || !r.candidate {
		return develop.ErrPackagePolicyUnverified
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.db == nil {
		return ErrDatabaseRequired
	}
	return nil
}

func (r *PackageRepository) FindPublishedPackage(ctx context.Context, slug string) (develop.PackageRecord, error) {
	if r == nil || !r.candidate {
		return develop.PackageRecord{}, develop.ErrPackagePolicyUnverified
	}
	if err := ctx.Err(); err != nil {
		return develop.PackageRecord{}, err
	}
	if err := develop.ValidatePackageSlug(slug); err != nil {
		return develop.PackageRecord{}, err
	}
	if err := r.check(ctx); err != nil {
		return develop.PackageRecord{}, err
	}
	var result develop.PackageRecord
	err := r.db.QueryRowContext(ctx, `SELECT m.id::text,m.slug,m.package_storage_key,m.package_url
 FROM mini_apps m
 JOIN dev_projects p ON p.id=m.project_id AND p.deleted_at IS NULL
 JOIN developers d ON d.id=p.developer_id
 WHERE m.deleted_at IS NULL AND m.stage=$1 AND lower(m.slug)=$2 LIMIT 1`, r.productionStage, strings.ToLower(slug)).Scan(&result.ID, &result.Slug, &result.StorageKey, &result.URL)
	if errors.Is(err, sql.ErrNoRows) {
		return develop.PackageRecord{}, develop.ErrPackageNotFound
	}
	if err != nil {
		return develop.PackageRecord{}, err
	}
	return result, nil
}

// IncrementPackageDownloads is one atomic, autocommitted statement. The source
// ignores ExecuteUpdateAsync's affected count, so zero matched rows is not an
// error. Stage/project filters apply to lookup, not to this subsequent update.
// Storage/redirect failures later cannot roll back this committed increment.
func (r *PackageRepository) IncrementPackageDownloads(ctx context.Context, id string) error {
	if err := r.check(ctx); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE mini_apps SET download_count=download_count+1 WHERE id=$1 AND deleted_at IS NULL`, id)
	return err
}
