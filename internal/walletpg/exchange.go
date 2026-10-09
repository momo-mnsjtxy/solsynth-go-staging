// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	a "solsynth.local/go-migration/ports/wallet/accounts"
	"sort"
	"time"
)

//go:embed exchange_schema.sql
var ExchangeSchemaSQL string

func (r *Repository) WithExchangePolicy(p *a.ExchangePolicy) *Repository { r.exchange = p; return r }
func (r *Repository) QuoteExchange(currency string, amount a.Amount) (a.ExchangeQuote, error) {
	if r == nil {
		return a.ExchangeQuote{}, a.ErrUnavailable
	}
	return r.exchange.Quote(currency, amount)
}
func (r *Repository) exchangePolicy(ctx context.Context, q querier, locked bool) error {
	if r == nil || r.exchange == nil {
		return a.ErrUnavailable
	}
	suffix := ""
	if locked {
		suffix = " FOR SHARE OF active, policy"
	}
	var version, hash string
	var enabled bool
	e := q.QueryRowContext(ctx, `SELECT active.version,policy.config_hash,active.enabled FROM wallet_v1.exchange_active_policy active JOIN wallet_v1.exchange_policy_versions policy ON policy.version=active.version WHERE active.singleton`+suffix).Scan(&version, &hash, &enabled)
	if e != nil {
		return a.ErrUnavailable
	}
	if !enabled || version != r.exchange.Config().Version || hash != r.exchange.Digest() {
		return a.ErrUnavailable
	}
	return nil
}
func (r *Repository) exchangeTreasuryActive(ctx context.Context, q querier) error {
	var active bool
	e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=$1 AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=$2)AND(p.expires_at IS NULL OR p.expires_at>$2)))`, r.exchange.Config().TreasuryAccountID, r.now().UTC()).Scan(&active)
	if e != nil {
		return e
	}
	if !active {
		return a.ErrForbidden
	}
	return nil
}
func (r *Repository) ExchangeReady(ctx context.Context) error {
	if r == nil || r.exchange == nil {
		return nil
	}
	if r.db == nil || r.authority == nil || r.payments == nil {
		return a.ErrUnavailable
	}
	if e := r.exchangePolicy(ctx, r.db, false); e != nil {
		return e
	}
	if e := r.exchangeTreasuryActive(ctx, r.db); e != nil {
		return a.ErrUnavailable
	}
	cfg := r.exchange.Config()
	var valid bool
	e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.id=$1 AND a.id=$2 AND w.deleted_at IS NULL AND NOT a.disabled)`, cfg.TreasuryWalletID, cfg.TreasuryAccountID).Scan(&valid)
	if e != nil || !valid {
		return a.ErrUnavailable
	}
	_, e = r.db.ExecContext(ctx, `SELECT policy_version,policy_hash,quote,debit_transaction_id,credit_transaction_id FROM wallet_v1.exchanges LIMIT 0`)
	return e
}
func (r *Repository) Exchange(ctx context.Context, c a.ExchangeCommand) (out a.ExchangeResponse, e error) {
	if r == nil || r.exchange == nil || r.authority == nil || r.payments == nil {
		return out, a.ErrUnavailable
	}
	if !a.ValidID(c.ActorID) || !a.ValidID(c.WalletID) || !c.Owner.Valid() || !a.ValidExchangeKey(c.IdempotencyKey) || c.Fingerprint != a.ExchangeBinding(c) {
		return out, a.ErrInvalid
	}
	expected, e := r.exchange.Quote(c.Quote.SourceCurrency, c.Quote.SourceAmount)
	if e != nil || expected != c.Quote {
		return out, a.ErrConflict
	}
	if c.ActorID == c.Quote.TreasuryAccountID || c.WalletID == c.Quote.TreasuryWalletID {
		return out, a.ErrForbidden
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	// Preflight is not authority to mutate. Acquire both account locks in UUID
	// order, then authenticate/authorize again before any wallet observations.
	if _, e = r.authority.Preflight(ctx, r.db, tx, c.ActorID, a.PermissionTransactions); e != nil {
		return out, reportError(e)
	}
	ids := []string{c.ActorID, c.Quote.TreasuryAccountID}
	sort.Strings(ids)
	for _, id := range ids {
		var disabled bool
		e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&disabled)
		if errors.Is(e, sql.ErrNoRows) || e == nil && disabled {
			return out, a.ErrForbidden
		}
		if e != nil {
			return out, e
		}
	}
	if e = r.exchangeTreasuryActive(ctx, tx); e != nil {
		return out, e
	}
	if e = r.writeAuthority(ctx, tx, c.Owner, c.ActorID, a.PermissionTransactions); e != nil {
		return out, e
	}
	if e = r.payments.LockPayment(ctx, r.db, tx, c.ActorID, a.PaymentExchange, c.Fingerprint); e != nil {
		return out, reportError(e)
	}
	if e = r.exchangePolicy(ctx, tx, true); e != nil {
		return out, e
	}
	if e = lock(ctx, tx, "exchange:"+c.ActorID+":"+c.IdempotencyKey); e != nil {
		return out, e
	}
	var receipt, oldFingerprint, debit, creditID string
	e = tx.QueryRowContext(ctx, `SELECT id::text,fingerprint,debit_transaction_id::text,credit_transaction_id::text FROM wallet_v1.exchanges WHERE actor_id=$1 AND idempotency_key=$2`, c.ActorID, c.IdempotencyKey).Scan(&receipt, &oldFingerprint, &debit, &creditID)
	replay := e == nil
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if replay && oldFingerprint != c.Fingerprint {
		return out, a.ErrIdempotency
	}
	wallets, e := lockWallets(ctx, tx, c.WalletID, c.Quote.TreasuryWalletID)
	if e != nil {
		return out, e
	}
	if wallets[c.WalletID].Owner() != c.Owner || wallets[c.Quote.TreasuryWalletID].Owner() != (a.Owner{AccountID: c.Quote.TreasuryAccountID}) {
		return out, a.ErrForbidden
	}
	out = a.ExchangeResponse{WalletID: c.WalletID, SourceCurrency: c.Quote.SourceCurrency, SourceAmount: c.Quote.SourceAmount, TargetCurrency: c.Quote.TargetCurrency, TargetAmount: c.Quote.TargetAmount, PolicyVersion: c.Quote.PolicyVersion, RemainderMills: c.Quote.RemainderMills}
	if replay {
		out.DebitTransaction, e = scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1 AND purpose='exchange' AND managed_by=$2 AND deleted_at IS NULL`, debit, receipt))
		if e != nil {
			return a.ExchangeResponse{}, e
		}
		out.CreditTransaction, e = scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1 AND purpose='exchange' AND managed_by=$2 AND deleted_at IS NULL`, creditID, receipt))
		if e != nil {
			return a.ExchangeResponse{}, e
		}
	} else {
		// PostgreSQL persists microseconds. Return the same timestamps on the
		// first response as every persisted replay, even with a nanosecond clock.
		now := r.now().UTC().Truncate(time.Microsecond)
		receipt, e = uuid()
		if e != nil {
			return out, e
		}
		out.DebitTransaction, e = exchangeLeg(ctx, tx, receipt, c, c.WalletID, c.Quote.TreasuryWalletID, c.Quote.SourceCurrency, c.Quote.SourceAmount, now)
		if e != nil {
			return out, e
		}
		out.CreditTransaction, e = exchangeLeg(ctx, tx, receipt, c, c.Quote.TreasuryWalletID, c.WalletID, c.Quote.TargetCurrency, c.Quote.TargetAmount, now)
		if e != nil {
			return out, e
		}
		quote, e := json.Marshal(c.Quote)
		if e != nil {
			return out, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.exchanges(id,actor_id,wallet_id,treasury_wallet_id,policy_version,policy_hash,source_currency,target_currency,source_amount,target_amount,quote,debit_transaction_id,credit_transaction_id,idempotency_key,fingerprint,created_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, receipt, c.ActorID, c.WalletID, c.Quote.TreasuryWalletID, c.Quote.PolicyVersion, c.Quote.PolicyHash, c.Quote.SourceCurrency, c.Quote.TargetCurrency, c.Quote.SourceAmount, c.Quote.TargetAmount, quote, out.DebitTransaction.ID, out.CreditTransaction.ID, c.IdempotencyKey, c.Fingerprint, now)
		if e != nil {
			return out, e
		}
	}
	if e = r.writeAuthority(ctx, tx, c.Owner, c.ActorID, a.PermissionTransactions); e != nil {
		return a.ExchangeResponse{}, e
	}
	if e = r.payments.CheckPayment(ctx, r.db, tx, c.ActorID, a.PaymentExchange, c.Fingerprint); e != nil {
		return a.ExchangeResponse{}, reportError(e)
	}
	// Account locks freeze policy edits, but a scheduled disablement can become
	// active while later policy, wallet, ledger or receipt operations wait.
	if e = r.exchangeTreasuryActive(ctx, tx); e != nil {
		return a.ExchangeResponse{}, e
	}
	if e = tx.Commit(); e != nil {
		return a.ExchangeResponse{}, e
	}
	return out, nil
}
func exchangeLeg(ctx context.Context, tx *sql.Tx, receipt string, c a.ExchangeCommand, payer, payee, currency string, amount a.Amount, now time.Time) (t a.Transaction, e error) {
	if e = ensurePocket(ctx, tx, payer, currency, now); e != nil {
		return t, e
	}
	if e = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount-$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND amount >= $3`, payer, currency, amount, now)); e != nil {
		return t, e
	}
	if e = credit(ctx, tx, payee, currency, amount, now); e != nil {
		return t, e
	}
	id, e := uuid()
	if e != nil {
		return t, e
	}
	t = a.Transaction{ID: id, Type: 0, PayerWalletID: payer, PayeeWalletID: payee, Currency: currency, Amount: amount, Remarks: c.Remark, Status: a.Confirmed, ConfirmedAt: &now, CreatedAt: now, UpdatedAt: now}
	_, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,remarks,status,is_frozen,require_confirmation,confirmed_at,created_at,updated_at,fingerprint,purpose,managed_by)VALUES($1,$2,$3,$4,$5,$6,$7,2,false,false,$8,$8,$8,$9,'exchange',$10)`, id, c.ActorID, payer, payee, currency, amount, c.Remark, now, c.Fingerprint, receipt)
	if e != nil {
		return t, e
	}
	if e = event(ctx, tx, "wallet.transaction.created", id, t, now); e != nil {
		return t, e
	}
	if e = pocketEvent(ctx, tx, payer, currency, now); e != nil {
		return t, e
	}
	e = pocketEvent(ctx, tx, payee, currency, now)
	return t, e
}

var _ a.ExchangeRepository = (*Repository)(nil)
