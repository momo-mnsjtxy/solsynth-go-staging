// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
)

// FileReadProtection is a mandatory restriction on domain-bound files. Unlike
// an optional visibility grant it applies to owners and public files as well.
// Ordinary files return nil. A protected file outside its current domain
// audience returns media.ErrNotFound; underlying failures fail closed.
type FileReadProtection interface {
	ProtectFileReadInTransaction(context.Context, *sql.Tx, string, string) error
}

func (r *Repository) WithFileReadProtection(protection FileReadProtection) *Repository {
	r.fileReadProtection = protection
	return r
}
func (r *Repository) protectFileRead(ctx context.Context, tx *sql.Tx, actor, id string) error {
	if r.fileReadProtection == nil {
		return nil
	}
	return r.fileReadProtection.ProtectFileReadInTransaction(ctx, tx, actor, id)
}
