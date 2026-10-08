// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"database/sql"
	c "solsynth.local/go-migration/ports/admin/content"
)

// BeginGlobalTransaction reuses the canonical current Superuser + exact named
// permission gate. Callers own rollback/commit and must revalidate after domain
// lock waits. Planning is observation only, before the sorted account lock set.
func (r *Repository) BeginGlobalTransaction(ctx context.Context, actor, permission string, plan func(*sql.Tx) ([]string, error)) (*sql.Tx, error) {
	return r.begin(ctx, actor, permission, true, plan)
}
func (r *Repository) RequireGlobalInTransaction(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	return r.authorize(ctx, tx, actor, permission, true)
}

// InspectionInTransaction is reserved for already-global extension operations.
// It repeats the named global gate and returns the canonical admin projection.
func (r *Repository) InspectionInTransaction(ctx context.Context, tx *sql.Tx, actor string, kind c.Kind, ref string) (any, error) {
	if e := r.authorize(ctx, tx, actor, kind.Permission("read"), true); e != nil {
		return nil, e
	}
	return r.get(ctx, tx, kind, ref)
}
