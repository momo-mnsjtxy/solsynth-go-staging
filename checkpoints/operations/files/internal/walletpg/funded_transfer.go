// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"

	"solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

// FundedTransfer moves existing funds between two canonical wallets inside the
// caller's transaction. The caller must authorize the operation before calling
// it. This primitive deliberately has no force or mint path.
func FundedTransfer(ctx context.Context, tx *sql.Tx, actor, payer, payee, currency string, amount accounts.Amount, remark *string, key string, now time.Time) (accounts.Transaction, error) {
	return fundedTransfer(ctx, tx, actor, payer, payee, currency, amount, remark, key, now, true)
}

// FundedReversal restores already captured funds. A disabled recipient remains
// entitled to a refund, so this explicit financial-reversal seam skips only the
// new-credit eligibility check while retaining canonical wallet and balance locks.
func FundedReversal(ctx context.Context, tx *sql.Tx, actor, payer, payee, currency string, amount accounts.Amount, remark *string, key string, now time.Time) (accounts.Transaction, error) {
	return fundedTransfer(ctx, tx, actor, payer, payee, currency, amount, remark, key, now, false)
}

func fundedTransfer(ctx context.Context, tx *sql.Tx, actor, payer, payee, currency string, amount accounts.Amount, remark *string, key string, now time.Time, requireEligiblePayee bool) (accounts.Transaction, error) {
	var out accounts.Transaction
	if tx == nil || !accounts.ValidID(actor) || !accounts.ValidID(payer) || !accounts.ValidID(payee) || payer == payee || amount.Sign() <= 0 || !accounts.ValidCurrency(currency) || key == "" || len(key) > 128 || remark != nil && (len(*remark) > 4096 || strings.ContainsRune(*remark, 0)) {
		return out, accounts.ErrInvalid
	}
	fpInput, err := json.Marshal(struct {
		Payer, Payee, Currency, Amount string
		Remark                         *string
	}{payer, payee, currency, amount.String(), remark})
	if err != nil {
		return out, err
	}
	fpBytes := sha256.Sum256(fpInput)
	fp := hex.EncodeToString(fpBytes[:])
	if err := lock(ctx, tx, "idempotency:"+actor+":"+key); err != nil {
		return out, err
	}
	out, err = scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE actor_id=$1::uuid AND idempotency_key=$2 AND deleted_at IS NULL`, actor, key))
	if err == nil {
		var prior string
		if e := tx.QueryRowContext(ctx, `SELECT fingerprint FROM wallet_v1.transactions WHERE id=$1::uuid`, out.ID).Scan(&prior); e != nil {
			return accounts.Transaction{}, e
		}
		if prior != fp {
			return accounts.Transaction{}, accounts.ErrIdempotency
		}
		return out, nil
	}
	if !errors.Is(err, accounts.ErrNotFound) {
		return accounts.Transaction{}, err
	}
	var payeeOwner accounts.Owner
	if requireEligiblePayee {
		payeeOwner, err = lockEligibleFundedPayee(ctx, tx, payee)
		if err != nil {
			return out, err
		}
	}
	wallets, err := lockWallets(ctx, tx, payer, payee)
	if err != nil {
		return out, err
	}
	if requireEligiblePayee && wallets[payee].Owner() != payeeOwner {
		return out, accounts.ErrConflict
	}
	// Wallet acquisition can wait past a scheduled disable_account boundary.
	// Re-read eligibility using PostgreSQL's current wall clock while the owner
	// and wallet locks are both retained.
	if requireEligiblePayee {
		if err = fundedPayeeEligible(ctx, tx, payeeOwner, false); err != nil {
			return out, err
		}
	}
	if err = ensurePocket(ctx, tx, payer, currency, now); err != nil {
		return out, err
	}
	if err = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount-$3,updated_at=$4 WHERE wallet_id=$1::uuid AND currency=$2 AND deleted_at IS NULL AND amount>=$3`, payer, currency, amount, now)); err != nil {
		return out, err
	}
	if err = credit(ctx, tx, payee, currency, amount, now); err != nil {
		return out, err
	}
	id, err := uuid()
	if err != nil {
		return out, err
	}
	out = accounts.Transaction{ID: id, PayerWalletID: payer, PayeeWalletID: payee, Currency: currency, Amount: amount, Remarks: remark, Status: accounts.Confirmed, ConfirmedAt: &now, CreatedAt: now, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,remarks,status,is_frozen,require_confirmation,confirmed_at,created_at,updated_at,idempotency_key,fingerprint,purpose) VALUES($1,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,2,false,false,$8,$8,$8,$9,$10,'transfer')`, id, actor, payer, payee, currency, amount, remark, now, key, fp)
	if err != nil {
		return accounts.Transaction{}, err
	}
	if err = event(ctx, tx, "wallet.transaction.created", id, out, now); err != nil {
		return accounts.Transaction{}, err
	}
	if err = pocketEvent(ctx, tx, payer, currency, now); err != nil {
		return accounts.Transaction{}, err
	}
	if err = pocketEvent(ctx, tx, payee, currency, now); err != nil {
		return accounts.Transaction{}, err
	}
	return out, nil
}

func lockEligibleFundedPayee(ctx context.Context, tx *sql.Tx, payee string) (accounts.Owner, error) {
	var account, realm sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT account_id::text,realm_id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND deleted_at IS NULL`, payee).Scan(&account, &realm); err != nil {
		return accounts.Owner{}, mapErr(err)
	}
	owner := accounts.Owner{}
	if account.Valid {
		owner.AccountID = account.String
	} else if realm.Valid {
		owner.RealmID = realm.String
	} else {
		return accounts.Owner{}, accounts.ErrNotFound
	}
	if err := fundedPayeeEligible(ctx, tx, owner, true); err != nil {
		return accounts.Owner{}, err
	}
	return owner, nil
}

func fundedPayeeEligible(ctx context.Context, tx *sql.Tx, owner accounts.Owner, lock bool) error {
	var eligible bool
	var err error
	suffix := ""
	if lock {
		suffix = " FOR SHARE NOWAIT"
	}
	if owner.AccountID != "" {
		err = tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid`+suffix, owner.AccountID).Scan(&eligible)
	} else if owner.RealmID != "" {
		if lock {
			suffix = " FOR SHARE OF r,a NOWAIT"
		}
		err = tx.QueryRowContext(ctx, `SELECT r.deleted_at IS NULL AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.id=$1::uuid`+suffix, owner.RealmID).Scan(&eligible)
	} else {
		return accounts.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return accounts.ErrConflict
	}
	if err != nil {
		return mapErr(err)
	}
	if !eligible {
		return accounts.ErrForbidden
	}
	return nil
}
