// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

func (r *Repository) CreateAppPayout(ctx context.Context, req c.AppPayout, key string) (wallet.Transaction, error) {
	var out wallet.Transaction
	if key == "" || len(key) > 128 || !wallet.ValidID(req.PayeeAccountID) || req.Amount.Sign() <= 0 || !wallet.ValidCurrency(req.Currency) {
		return out, c.ErrInvalid
	}
	tx, err := r.begin(ctx, "", "")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	app, err := r.client(ctx, tx, req.Credentials, "orders.payouts.manage")
	if err != nil {
		return out, err
	}
	var payer, owner, payee string
	// Lock the merchant alone before resolving its wallet. If a rebinding wins
	// while this statement waits, READ COMMITTED returns the new binding. Joining
	// the wallet in this locking statement can instead retain an old join candidate
	// during row rechecking and falsely report that the rebound merchant is absent.
	err = tx.QueryRowContext(ctx, `SELECT m.wallet_id::text FROM commerce_v1.merchants m WHERE m.app_id=$1::uuid AND m.wallet_id IS NOT NULL FOR SHARE OF m`, app.AppID).Scan(&payer)
	if errors.Is(err, sql.ErrNoRows) {
		return out, c.ErrConflict
	}
	if err != nil {
		return out, err
	}
	// Keep wallet acquisition in FundedTransfer's single UUID-sorted lock phase;
	// the retained merchant lock already prevents another treasury rebinding.
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(account_id::text,'') FROM wallet_v1.wallets WHERE id=$1::uuid AND deleted_at IS NULL`, payer).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return out, c.ErrConflict
	}
	if err != nil {
		return out, err
	}
	if owner == req.PayeeAccountID {
		return out, c.ErrForbidden
	}
	err = tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, req.PayeeAccountID).Scan(&payee)
	if errors.Is(err, sql.ErrNoRows) {
		return out, c.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	scoped := sha256.Sum256([]byte("app-payout:" + app.AppID + ":" + key))
	out, err = walletpg.FundedTransfer(ctx, tx, app.AppID, payer, payee, req.Currency, req.Amount, req.Remarks, hex.EncodeToString(scoped[:]), r.now().UTC())
	if err != nil {
		return out, err
	}
	if _, err = r.client(ctx, tx, req.Credentials, "orders.payouts.manage"); err != nil {
		return wallet.Transaction{}, err
	}
	return out, tx.Commit()
}
