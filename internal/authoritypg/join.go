// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
)

// JoinTransaction is for trusted adapters using this repository's database.
// The caller owns commit/rollback and must supply a transaction from db. The
// account row is locked in the same order used by authority and domain guards.
func (r *Repository) JoinTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, id string) (authority.Transaction, error) {
	if r == nil || r.db == nil || r.db != db || tx == nil || !authority.ValidID(id) {
		return nil, authority.ErrInvalid
	}
	var isolation string
	if e := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); e != nil || isolation != "read committed" {
		return nil, authority.ErrUnavailable
	}
	var a authority.Account
	e := tx.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,is_superuser,disabled,version,created_at FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.Superuser, &a.Disabled, &a.Version, &a.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, authority.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	return &transaction{tx: tx, ctx: ctx, account: a}, nil
}
