// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
)

// ReadVisibility can extend private-file reads to an authorized parent resource.
// It runs after the canonical ready-file and owner-state checks. Implementations
// must query this transaction only, without writes, new locks, external I/O, or
// accepting asserted identities. Missing policy retains owner/public semantics.
type ReadVisibility interface {
	CanReadInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
}

func (r *Repository) WithReadVisibility(v ReadVisibility) *Repository { r.readVisibility = v; return r }

// AnyReadVisibility combines independent parent-resource grants, failing closed
// on errors instead of interpreting an unavailable policy as authorization.
type AnyReadVisibility []ReadVisibility

// Ready preserves the dependency probes of composed resource policies. Pure
// policies may omit a probe; constructors still never install schemas.
func (v AnyReadVisibility) Ready(ctx context.Context) error {
	for _, policy := range v {
		if ready, ok := policy.(interface{ Ready(context.Context) error }); ok {
			if err := ready.Ready(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v AnyReadVisibility) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	for _, policy := range v {
		if policy == nil {
			continue
		}
		yes, e := policy.CanReadInTransaction(ctx, tx, actor, id)
		if e != nil {
			return false, e
		}
		if yes {
			return true, nil
		}
	}
	return false, nil
}
