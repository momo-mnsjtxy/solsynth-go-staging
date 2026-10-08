// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	"errors"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/wallet/accounts"
)

// Authority, then realm grants, then existing domain locks is the request-write
// lock order. Maintenance transitions do not represent an authenticated request.
func (r *Repository) writeAuthority(ctx context.Context, tx *sql.Tx, owner accounts.Owner, actor, permission string) error {
	if r.authority == nil {
		return nil // Explicit legacy/separate-pool composition only.
	}
	var p authority.Principal
	var err error
	if permission == "" {
		p, err = r.authority.Authenticate(ctx, r.db, tx, actor)
	} else {
		p, err = r.authority.Authorize(ctx, r.db, tx, actor, permission)
	}
	if err != nil {
		var failure *authority.Failure
		if errors.As(err, &failure) {
			if failure.Status == 401 {
				return accounts.ErrUnauthorized
			}
			if failure.Status == 403 || failure.Status == 423 {
				return accounts.ErrForbidden
			}
		}
		return accounts.ErrUnavailable
	}
	if !owner.Valid() {
		return accounts.ErrInvalid
	}
	if owner.AccountID != "" {
		if owner.AccountID != p.Account.ID {
			return accounts.ErrForbidden
		}
		return nil
	}
	// A realm permission checked by the service before the SQL transaction may
	// already be stale. Lock and recheck the actual grant through domain commit.
	var role string
	err = tx.QueryRowContext(ctx, `SELECT role FROM wallet_v1.realm_grants WHERE account_id=$1 AND realm_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>statement_timestamp()) FOR SHARE`, p.Account.ID, owner.RealmID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.ErrForbidden
	}
	if err != nil {
		return err
	}
	if role != "editor" {
		return accounts.ErrForbidden
	}
	return nil
}
