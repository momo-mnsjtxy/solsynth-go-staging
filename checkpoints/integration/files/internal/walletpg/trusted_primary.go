// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/wallet/accounts"
)

// EnsurePrimaryWalletInTransaction resolves an account's canonical default
// wallet, creating its first wallet when none exists. It is a trusted service
// seam: the caller must already hold the active account row FOR UPDATE in tx.
// The helper rechecks current account eligibility, serializes with ordinary
// wallet creation, preserves the primary-first/oldest fallback rule, and never
// creates funds or pockets. The canonical account lock plus owner advisory lock
// stabilize an existing default, so this lookup must not take a wallet lock
// ahead of FundedTransfer's single UUID-sorted wallet lock phase.
func EnsurePrimaryWalletInTransaction(ctx context.Context, tx *sql.Tx, account string, now time.Time) (accounts.Wallet, error) {
	if tx == nil || !accounts.ValidID(account) {
		return accounts.Wallet{}, accounts.ErrInvalid
	}
	owner := accounts.Owner{AccountID: account}
	if err := fundedPayeeEligible(ctx, tx, owner, false); err != nil {
		return accounts.Wallet{}, err
	}
	if err := lock(ctx, tx, "owner:"+owner.Key()); err != nil {
		return accounts.Wallet{}, err
	}
	w, err := scanWallet(tx.QueryRowContext(ctx, `SELECT `+walletCols+` FROM wallet_v1.wallets WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, account))
	if err == nil {
		return w, nil
	}
	if !errors.Is(err, accounts.ErrNotFound) {
		return accounts.Wallet{}, err
	}
	return createWalletInTransaction(ctx, tx, owner, "Wallet", now.UTC())
}

// createWalletInTransaction requires the owner advisory lock. Keeping quota,
// primary selection, persistence and outbox creation here prevents trusted
// provisioning from drifting from the public wallet model.
func createWalletInTransaction(ctx context.Context, tx *sql.Tx, owner accounts.Owner, name string, now time.Time) (accounts.Wallet, error) {
	var count int
	var hasPrimary bool
	err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(bool_or(is_primary),false) FROM wallet_v1.wallets WHERE deleted_at IS NULL AND (account_id=$1 OR realm_id=$2)`, nullable(owner.AccountID), nullable(owner.RealmID)).Scan(&count, &hasPrimary)
	if err != nil {
		return accounts.Wallet{}, err
	}
	if owner.AccountID != "" && count >= 3 {
		return accounts.Wallet{}, accounts.ErrQuota
	}
	id, err := uuid()
	if err != nil {
		return accounts.Wallet{}, err
	}
	w, err := scanWallet(tx.QueryRowContext(ctx, `INSERT INTO wallet_v1.wallets(id,account_id,realm_id,name,is_primary,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6) RETURNING `+walletCols, id, nullable(owner.AccountID), nullable(owner.RealmID), name, !hasPrimary, now))
	if err != nil {
		return w, err
	}
	if err = event(ctx, tx, "wallet.created", w.ID, w, now); err != nil {
		return w, err
	}
	return w, nil
}
