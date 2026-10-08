// SPDX-License-Identifier: AGPL-3.0-only
package taxonomypg

import (
	"context"
	"database/sql"
	"strings"
)

// NewWithIndependentPolicy uses owned_taxonomy and canonical owned_publishers.
// Candidate fixtures retain their original separate namespace. All identifiers
// are fixed literals; no caller-supplied schema/table name is accepted.
func NewWithIndependentPolicy(db *sql.DB, publicVisibility int32) *Repository {
	return &Repository{db: db, synthetic: true, owned: true, publicVisibility: publicVisibility}
}

// policyTx changes only fixed schema-qualified identifiers in authored SQL.
// User values remain positional arguments, never part of rewritten SQL.
type policyTx struct {
	*sql.Tx
	owned bool
}

func policySQL(query string, owned bool) string {
	if !owned {
		return query
	}
	query = strings.ReplaceAll(query, "taxonomy_candidate.publishers", "owned_publishers")
	return strings.ReplaceAll(query, "taxonomy_candidate.", "owned_taxonomy.")
}
func (r *Repository) begin(ctx context.Context, opts *sql.TxOptions) (*policyTx, error) {
	tx, err := r.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &policyTx{tx, r.owned}, nil
}
func (t *policyTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, policySQL(q, t.owned), args...)
}
func (t *policyTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, policySQL(q, t.owned), args...)
}
func (t *policyTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, policySQL(q, t.owned), args...)
}
