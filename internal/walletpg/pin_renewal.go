// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/wallet/accounts"
)

// Checks blocking policy only. Caller retains canonical account locking and
// validates the existing standing mandate; this never creates a mandate.
func (p *PINVerifier) AllowRenewal(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) (bool, error) {
	if p == nil || p.db == nil || p.db != db || tx == nil || !accounts.ValidID(actor) {
		return false, accounts.ErrUnavailable
	}
	var disabled bool
	var linked sql.NullString
	e := tx.QueryRowContext(ctx, `SELECT disabled,factor_id::text FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, actor).Scan(&disabled, &linked)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil || disabled {
		return false, e
	}
	if linked.Valid {
		var active bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_factors WHERE id=$1::uuid AND account_id=$2::uuid AND type=4 AND enabled_at IS NOT NULL)`, linked.String, actor).Scan(&active)
		return active, e
	}
	return true, nil
}
