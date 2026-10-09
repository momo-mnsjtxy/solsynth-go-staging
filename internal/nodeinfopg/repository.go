// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed translation of licensed DysonNetwork NodeInfoController.cs
// at 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.
package nodeinfopg

import (
	"context"
	"database/sql"
	"errors"

	"solsynth.local/go-migration/ports/sphere/nodeinfo"
)

var (
	ErrUnverifiedPolicy        = errors.New("nodeinfopg: global filters and Fediverse enum mapping unverified; repository disabled")
	ErrDatabaseRequired        = errors.New("nodeinfopg: database required")
	ErrCandidatePolicyRequired = errors.New("nodeinfopg: explicit soft-delete candidate policy required")
)

type SoftDeletePolicy string

// CandidateExcludeDeletedRows is an explicit synthetic policy selection, not
// an assertion that the excluded Shared ApplySoftDeleteFilters implementation
// is equivalent to this condition.
const CandidateExcludeDeletedRows SoftDeletePolicy = "deleted-at-is-null"

// Repository is read-only and owns neither its connection nor the schema.
type Repository struct {
	db            *sql.DB
	candidate     bool
	fediverseType int32
}

var _ nodeinfo.Reader = (*Repository)(nil)

// New is disabled before any database operation, even with a valid connection.
func New(db *sql.DB) *Repository { return &Repository{db: db} }

// NewSyntheticCompatibilityRepository requires both a caller-provided SYNTHETIC
// Fediverse type and an explicit candidate soft-delete policy. No numeric enum
// value is inferred from the excluded Shared model or protocol definitions.
func NewSyntheticCompatibilityRepository(db *sql.DB, fediverseType int32, policy SoftDeletePolicy) (*Repository, error) {
	if policy != CandidateExcludeDeletedRows {
		return nil, ErrCandidatePolicyRequired
	}
	return &Repository{db: db, candidate: true, fediverseType: fediverseType}, nil
}

// ReadStats executes the source's three counts in their original order. It does
// not add a transaction: concurrent writes can be visible between counts as in
// the controller. Count overflow fails rather than truncating the int32 result.
// It intentionally neither joins publishers for post counts nor adds activity,
// visibility, publication-time, moderation or URI-content predicates.
func (r *Repository) ReadStats(ctx context.Context) (nodeinfo.Stats, error) {
	if r == nil || !r.candidate {
		return nodeinfo.Stats{}, ErrUnverifiedPolicy
	}
	if err := ctx.Err(); err != nil {
		return nodeinfo.Stats{}, err
	}
	if r.db == nil {
		return nodeinfo.Stats{}, ErrDatabaseRequired
	}
	var stats nodeinfo.Stats
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM publishers WHERE deleted_at IS NULL`).Scan(&stats.TotalPublishers); err != nil {
		return nodeinfo.Stats{}, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM posts WHERE deleted_at IS NULL AND publisher_id IS NOT NULL`).Scan(&stats.TotalPosts); err != nil {
		return nodeinfo.Stats{}, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM publishers WHERE deleted_at IS NULL AND type <> $1 AND uri IS NOT NULL`, r.fediverseType).Scan(&stats.ActivePublishers); err != nil {
		return nodeinfo.Stats{}, err
	}
	if err := ctx.Err(); err != nil {
		return nodeinfo.Stats{}, err
	}
	return stats, nil
}
