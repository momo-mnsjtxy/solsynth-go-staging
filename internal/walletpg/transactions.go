// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

const transactionCols = `CASE WHEN purpose='commerce' THEN 1 ELSE 0 END,id::text,payer_wallet_id::text,payee_wallet_id::text,currency,amount::text,remarks,status,is_frozen,require_confirmation,frozen_at,expires_at,confirmed_at,confirmation_received_at,created_at,updated_at,deleted_at`

func scanTransaction(row scanner) (accounts.Transaction, error) {
	var t accounts.Transaction
	e := row.Scan(&t.Type, &t.ID, &t.PayerWalletID, &t.PayeeWalletID, &t.Currency, &t.Amount, &t.Remarks, &t.Status, &t.IsFrozen, &t.RequireConfirmation, &t.FrozenAt, &t.ExpiresAt, &t.ConfirmedAt, &t.ConfirmationReceivedAt, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	return t, mapErr(e)
}
func (r *Repository) Transfer(ctx context.Context, c accounts.TransferCommand) (accounts.Transaction, error) {
	if !accounts.ValidID(c.ActorID) || !accounts.ValidID(c.PayerWalletID) || !accounts.ValidID(c.PayeeWalletID) || c.PayerWalletID == c.PayeeWalletID || !c.PayerOwner.Valid() || c.Amount.Sign() <= 0 || len(c.Currency) == 0 || len(c.Fingerprint) != 64 || len(c.IdempotencyKey) > 128 {
		return accounts.Transaction{}, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return accounts.Transaction{}, e
	}
	defer tx.Rollback()
	if e = r.writeAuthority(ctx, tx, c.PayerOwner, c.ActorID, accounts.PermissionTransactions); e != nil {
		return accounts.Transaction{}, e
	}
	if c.IdempotencyKey != "" {
		if e = lock(ctx, tx, "idempotency:"+c.ActorID+":"+c.IdempotencyKey); e != nil {
			return accounts.Transaction{}, e
		}
		var id, fingerprint string
		e = tx.QueryRowContext(ctx, `SELECT id::text,fingerprint FROM wallet_v1.transactions WHERE actor_id=$1 AND idempotency_key=$2`, c.ActorID, c.IdempotencyKey).Scan(&id, &fingerprint)
		if e == nil {
			if fingerprint != c.Fingerprint {
				return accounts.Transaction{}, accounts.ErrIdempotency
			}
			original, x := scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1 AND deleted_at IS NULL`, id))
			if x != nil {
				return original, x
			}
			if original.PayerWalletID != c.PayerWalletID || original.PayeeWalletID != c.PayeeWalletID {
				return original, accounts.ErrIdempotency
			}
			wallets, x := lockWallets(ctx, tx, original.PayerWalletID, original.PayeeWalletID)
			if x != nil {
				return original, x
			}
			if wallets[original.PayerWalletID].Owner() != c.PayerOwner {
				return original, accounts.ErrForbidden
			}
			return original, tx.Commit()
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return accounts.Transaction{}, e
		}
	}
	wallets, e := lockWallets(ctx, tx, c.PayerWalletID, c.PayeeWalletID)
	if e != nil {
		return accounts.Transaction{}, e
	}
	if wallets[c.PayerWalletID].Owner() != c.PayerOwner {
		return accounts.Transaction{}, accounts.ErrForbidden
	}
	now := r.now().UTC()
	id, e := uuid()
	if e != nil {
		return accounts.Transaction{}, e
	}
	status, frozen, expires, confirmed := accounts.InitialStatus(c.Freeze, c.RequireConfirmation, now)
	t := accounts.Transaction{ID: id, PayerWalletID: c.PayerWalletID, PayeeWalletID: c.PayeeWalletID, Currency: c.Currency, Amount: c.Amount, Remarks: c.Remark, Status: status, IsFrozen: c.Freeze, RequireConfirmation: c.RequireConfirmation, FrozenAt: frozen, ExpiresAt: expires, ConfirmedAt: confirmed, CreatedAt: now, UpdatedAt: now}
	if e = ensurePocket(ctx, tx, c.PayerWalletID, c.Currency, now); e != nil {
		return t, e
	}
	if status == accounts.Confirmed {
		e = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount-$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND amount >= $3`, c.PayerWalletID, c.Currency, c.Amount, now))
		if e != nil {
			return t, e
		}
		if e = credit(ctx, tx, c.PayeeWalletID, c.Currency, c.Amount, now); e != nil {
			return t, e
		}
	} else {
		e = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount-$3,held_amount=held_amount+$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND amount >= $3`, c.PayerWalletID, c.Currency, c.Amount, now))
		if e != nil {
			return t, e
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,remarks,status,is_frozen,require_confirmation,frozen_at,expires_at,confirmed_at,created_at,updated_at,idempotency_key,fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14,$15,$16)`, t.ID, c.ActorID, t.PayerWalletID, t.PayeeWalletID, t.Currency, t.Amount, t.Remarks, t.Status, t.IsFrozen, t.RequireConfirmation, t.FrozenAt, t.ExpiresAt, t.ConfirmedAt, now, nullable(c.IdempotencyKey), c.Fingerprint)
	if e != nil {
		return t, e
	}
	if e = event(ctx, tx, "wallet.transaction.created", t.ID, t, now); e != nil {
		return t, e
	}
	if e = pocketEvent(ctx, tx, t.PayerWalletID, t.Currency, now); e != nil {
		return t, e
	}
	if status == accounts.Confirmed {
		if e = pocketEvent(ctx, tx, t.PayeeWalletID, t.Currency, now); e != nil {
			return t, e
		}
	}
	return t, tx.Commit()
}
func credit(ctx context.Context, tx *sql.Tx, id, currency string, amount accounts.Amount, now time.Time) error {
	if e := ensurePocket(ctx, tx, id, currency, now); e != nil {
		return e
	}
	return changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount+$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL`, id, currency, amount, now))
}
func (r *Repository) GetTransaction(ctx context.Context, id string) (accounts.Transaction, error) {
	if !accounts.ValidID(id) {
		return accounts.Transaction{}, accounts.ErrInvalid
	}
	if r == nil || r.db == nil {
		return accounts.Transaction{}, accounts.ErrUnavailable
	}
	return scanTransaction(r.db.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1 AND deleted_at IS NULL`, id))
}

const transactionWhere = `deleted_at IS NULL AND (payer_wallet_id=$1 OR payee_wallet_id=$1) AND ($2='' OR ($2='in' AND payee_wallet_id=$1) OR ($2='out' AND payer_wallet_id=$1)) AND ($3::integer IS NULL OR $3=CASE WHEN purpose='commerce' THEN 1 ELSE 0 END) AND (NOT $4 OR (payee_wallet_id=$1 AND status IN (0,1) AND require_confirmation))`

func (r *Repository) Transactions(ctx context.Context, q accounts.TransactionQuery) (accounts.TransactionPage, error) {
	out := accounts.TransactionPage{Items: []accounts.Transaction{}}
	if !accounts.ValidID(q.WalletID) || q.Offset < 0 || q.Take < 0 || q.Take > 100 {
		return out, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = wallet(ctx, tx, q.WalletID, false); e != nil {
		return out, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM wallet_v1.transactions WHERE `+transactionWhere, q.WalletID, q.Direction, q.Type, q.PendingOnly).Scan(&out.Total); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE `+transactionWhere+` ORDER BY created_at DESC,id DESC OFFSET $5 LIMIT $6`, q.WalletID, q.Direction, q.Type, q.PendingOnly, q.Offset, q.Take)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		t, x := scanTransaction(rows)
		if x != nil {
			rows.Close()
			return out, x
		}
		out.Items = append(out.Items, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) Transition(ctx context.Context, id string, payee accounts.Owner, action accounts.Transition) (accounts.Transaction, error) {
	if !accounts.ValidID(id) || !payee.Valid() {
		return accounts.Transaction{}, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return accounts.Transaction{}, e
	}
	defer tx.Rollback()
	if e = r.writeAuthority(ctx, tx, payee, "", accounts.PermissionTransactions); e != nil {
		return accounts.Transaction{}, e
	}
	old, e := scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1 AND deleted_at IS NULL AND managed_by IS NULL FOR UPDATE`, id))
	if e != nil {
		return old, e
	}
	wallets, e := lockWallets(ctx, tx, old.PayerWalletID, old.PayeeWalletID)
	if e != nil {
		return old, e
	}
	if wallets[old.PayeeWalletID].Owner() != payee {
		return old, accounts.ErrForbidden
	}
	next, effect, e := accounts.Advance(old, action, r.now().UTC())
	if e != nil {
		return old, e
	}
	if e = apply(ctx, tx, old, next, effect, false); e != nil {
		return old, e
	}
	return next, tx.Commit()
}
func apply(ctx context.Context, tx *sql.Tx, old, next accounts.Transaction, effect accounts.Effect, expired bool) error {
	now := next.UpdatedAt
	if effect == accounts.Release {
		if e := changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET held_amount=held_amount-$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND held_amount >= $3`, old.PayerWalletID, old.Currency, old.Amount, now)); e != nil {
			return fmt.Errorf("held balance invariant: %w", e)
		}
		if e := credit(ctx, tx, old.PayeeWalletID, old.Currency, old.Amount, now); e != nil {
			return e
		}
	}
	if effect == accounts.Refund {
		if e := changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount+$3,held_amount=held_amount-$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND held_amount >= $3`, old.PayerWalletID, old.Currency, old.Amount, now)); e != nil {
			return fmt.Errorf("held balance invariant: %w", e)
		}
	}
	if effect == accounts.NoEffect && old.ConfirmationReceivedAt != nil {
		return nil
	}
	if effect == accounts.NoEffect && next.ConfirmationReceivedAt == nil {
		return nil
	}
	if _, e := tx.ExecContext(ctx, `UPDATE wallet_v1.transactions SET status=$2,confirmed_at=$3,confirmation_received_at=$4,updated_at=$5 WHERE id=$1`, next.ID, next.Status, next.ConfirmedAt, next.ConfirmationReceivedAt, now); e != nil {
		return e
	}
	kind := "wallet.transaction.confirmation_received"
	if effect == accounts.Release {
		kind = "wallet.transaction.confirmed"
	} else if effect == accounts.Refund {
		kind = "wallet.transaction.refunded"
	}
	if e := event(ctx, tx, kind, next.ID, next, now); e != nil {
		return e
	}
	if expired && effect == accounts.Refund {
		if e := event(ctx, tx, "wallet.transaction.expired", next.ID, next, now); e != nil {
			return e
		}
	}
	if effect != accounts.NoEffect {
		if e := pocketEvent(ctx, tx, next.PayerWalletID, next.Currency, now); e != nil {
			return e
		}
		if effect == accounts.Release {
			return pocketEvent(ctx, tx, next.PayeeWalletID, next.Currency, now)
		}
	}
	return nil
}

// ProcessDue is a bounded application maintenance operation, not an HTTP admin
// endpoint. Each item commits separately; a returned count includes only durable
// transitions. Multiple workers use SKIP LOCKED and deterministic wallet locks.
// No clock supplied by an HTTP caller is accepted.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, accounts.ErrInvalid
	}
	count := 0
	for count < limit {
		did, e := r.processOne(ctx)
		if e != nil {
			return count, e
		}
		if !did {
			return count, nil
		}
		count++
	}
	return count, nil
}
func (r *Repository) processOne(ctx context.Context) (bool, error) {
	tx, e := r.begin(ctx, false)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	now := r.now().UTC()
	old, e := scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE deleted_at IS NULL AND managed_by IS NULL AND status IN (0,1) AND expires_at<=$1 ORDER BY expires_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
	if errors.Is(e, accounts.ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if _, e = lockWallets(ctx, tx, old.PayerWalletID, old.PayeeWalletID); e != nil {
		return false, e
	}
	next, effect, e := accounts.Expire(old, now)
	if e != nil {
		return false, e
	}
	if e = apply(ctx, tx, old, next, effect, true); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return true, nil
}
