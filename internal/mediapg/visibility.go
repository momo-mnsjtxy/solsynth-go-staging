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
