// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"context"
	"database/sql"
)

// MediaStore joins an already authorized resource transaction. Implementations
// own references only; they must not change file ownership or public flags.
// Null and omitted source request IDs both retain the previous association.
type MediaStore interface {
	Apply(context.Context, *sql.Tx, string, string, string, *string, *string) error
	Load(context.Context, *sql.Tx, string, string, string) (map[string]any, map[string]any, error)
	Remove(context.Context, *sql.Tx, string, string) error
}
