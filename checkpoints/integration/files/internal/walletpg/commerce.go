// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"sort"
	"time"
)

// LockCommerceBatch acquires every transaction and wallet needed by a bounded
// settlement batch in one canonical order. Callers must already hold their own
// settlement rows; later CommerceSettle calls reuse these transaction locks.
func LockCommerceBatch(ctx context.Context, tx *sql.Tx, orders []string) error {
	if tx == nil {
		return accounts.ErrInvalid
	}
	ordered := append([]string(nil), orders...)
	sort.Strings(ordered)
	walletIDs := make([]string, 0, len(ordered)*2)
	previous := ""
	for _, order := range ordered {
		if !accounts.ValidID(order) {
			return accounts.ErrInvalid
		}
		if order == previous {
			continue
		}
		previous = order
		var payer, payee string
		if err := tx.QueryRowContext(ctx, `SELECT payer_wallet_id::text,payee_wallet_id::text FROM wallet_v1.transactions WHERE managed_by=$1::uuid AND purpose='commerce' AND deleted_at IS NULL FOR UPDATE`, order).Scan(&payer, &payee); err != nil {
			return mapErr(err)
		}
		walletIDs = append(walletIDs, payer, payee)
	}
	_, err := lockWallets(ctx, tx, walletIDs...)
	return err
}

// CommerceHold is an internal transaction composition primitive. The caller
// must hold its mandatory same-pool authority guard and commerce order row.
// It never commits, opens another connection, mints money or accepts a nil owner.
// managed_by prevents the ordinary transfer endpoints/jobs changing this hold.
func CommerceHold(ctx context.Context, tx *sql.Tx, order, actor, payer, payee, currency string, amount accounts.Amount, now time.Time) (accounts.Transaction, error) {
	var t accounts.Transaction
	if tx == nil || !accounts.ValidID(order) || !accounts.ValidID(actor) || !accounts.ValidID(payer) || !accounts.ValidID(payee) || payer == payee || amount.Sign() <= 0 || len(currency) == 0 || len(currency) > 128 {
		return t, accounts.ErrInvalid
	}
	wallets, err := lockWallets(ctx, tx, payer, payee)
	if err != nil {
		return t, err
	}
	if wallets[payer].AccountID == nil || *wallets[payer].AccountID != actor {
		return t, accounts.ErrForbidden
	}
	id, err := uuid()
	if err != nil {
		return t, err
	}
	t = accounts.Transaction{ID: id, Type: 1, PayerWalletID: payer, PayeeWalletID: payee, Currency: currency, Amount: amount, Status: accounts.Frozen, IsFrozen: true, RequireConfirmation: true, FrozenAt: &now, CreatedAt: now, UpdatedAt: now}
	if err = ensurePocket(ctx, tx, payer, currency, now); err != nil {
		return t, err
	}
	if err = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount-$3,held_amount=held_amount+$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND amount >= $3`, payer, currency, amount, now)); err != nil {
		return t, err
	}
	f := sha256.Sum256([]byte(order + ":" + payer + ":" + payee + ":" + currency + ":" + amount.String()))
	_, err = tx.ExecContext(ctx, `INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,status,is_frozen,require_confirmation,frozen_at,created_at,updated_at,fingerprint,purpose,managed_by) VALUES($1,$2,$3,$4,$5,$6,1,true,true,$7,$7,$7,$8,'commerce',$9)`, id, actor, payer, payee, currency, amount, now, hex.EncodeToString(f[:]), order)
	if err != nil {
		return t, err
	}
	if err = event(ctx, tx, "wallet.transaction.created", id, t, now); err != nil {
		return t, err
	}
	if err = pocketEvent(ctx, tx, payer, currency, now); err != nil {
		return t, err
	}
	return t, nil
}

// CommerceSettle consumes a durable hold created above, exactly once. A refund
// restores payer availability; release credits the configured payee. Both use
// the canonical wallet outbox and pocket conservation checks in apply.
func CommerceSettle(ctx context.Context, tx *sql.Tx, order string, refund bool, now time.Time) (accounts.Transaction, error) {
	var t accounts.Transaction
	if tx == nil || !accounts.ValidID(order) {
		return t, accounts.ErrInvalid
	}
	t, err := scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE managed_by=$1 AND purpose='commerce' AND deleted_at IS NULL FOR UPDATE`, order))
	if err != nil {
		return t, err
	}
	target, effect := accounts.Confirmed, accounts.Release
	if refund {
		target, effect = accounts.Refunded, accounts.Refund
	}
	if t.Status == target {
		return t, nil
	}
	if t.Status != accounts.Frozen {
		return t, accounts.ErrConflict
	}
	if _, err = lockWallets(ctx, tx, t.PayerWalletID, t.PayeeWalletID); err != nil {
		return t, err
	}
	next := t
	next.Status = target
	next.UpdatedAt = now
	if !refund {
		next.ConfirmedAt = &now
	}
	if err = apply(ctx, tx, t, next, effect, false); err != nil {
		return t, err
	}
	return next, nil
}
