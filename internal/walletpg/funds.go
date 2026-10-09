// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/wallet/accounts"
)

//go:embed funds_schema.sql
var FundsSchemaSQL string
var _ accounts.FundRepository = (*Repository)(nil)

const fundCols = `id::text,creator_account_id::text,beneficiary_wallet_id::text,currency,total_amount::text,amount_of_splits,split_type,message,status,is_open,is_raising,target_amount::text,contribution_type,contribution_amount::text,deadline_at,expires_at,created_at,updated_at`

func scanFund(row scanner) (accounts.Fund, error) {
	f := accounts.Fund{Recipients: []accounts.FundRecipient{}}
	e := row.Scan(&f.ID, &f.CreatorAccountID, &f.BeneficiaryWalletID, &f.Currency, &f.TotalAmount, &f.AmountOfSplits, &f.SplitType, &f.Message, &f.Status, &f.IsOpen, &f.IsRaising, &f.TargetAmount, &f.ContributionType, &f.ContributionAmount, &f.DeadlineAt, &f.ExpiredAt, &f.CreatedAt, &f.UpdatedAt)
	return f, mapErr(e)
}
func fundAt(ctx context.Context, q querier, id string, update bool) (accounts.Fund, error) {
	sql := `SELECT ` + fundCols + ` FROM wallet_v1.funds WHERE id=$1`
	if update {
		sql += ` FOR UPDATE`
	}
	return scanFund(q.QueryRowContext(ctx, sql, id))
}
func hydrateFund(ctx context.Context, q querier, f *accounts.Fund) error {
	var reserved, allocated accounts.Amount
	if e := q.QueryRowContext(ctx, `SELECT COALESCE(sum(amount),0)::text FROM wallet_v1.fund_reservations WHERE fund_id=$1`, f.ID).Scan(&reserved); e != nil {
		return e
	}
	if e := q.QueryRowContext(ctx, `SELECT COALESCE(sum(t.amount),0)::text FROM wallet_v1.fund_allocations a JOIN wallet_v1.fund_reservations r ON r.id=a.reservation_id JOIN wallet_v1.transactions t ON t.id=a.transaction_id WHERE r.fund_id=$1`, f.ID).Scan(&allocated); e != nil {
		return e
	}
	var e error
	if f.IsRaising {
		f.RaisedAmount = reserved
		f.RemainingAmount, e = f.TargetAmount.Sub(reserved)
	} else {
		f.RemainingAmount, e = reserved.Sub(allocated)
	}
	if e != nil {
		return e
	}
	rows, e := q.QueryContext(ctx, `SELECT p.id::text,p.fund_id::text,p.account_id::text,COALESCE(r.amount,t.amount,0)::text,p.received_at,p.transaction_id::text FROM wallet_v1.fund_participants p LEFT JOIN wallet_v1.fund_reservations r ON r.id=p.reservation_id LEFT JOIN wallet_v1.transactions t ON t.id=p.transaction_id WHERE p.fund_id=$1 ORDER BY p.id`, f.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	f.Recipients = []accounts.FundRecipient{}
	for rows.Next() {
		var v accounts.FundRecipient
		if e = rows.Scan(&v.ID, &v.FundID, &v.RecipientAccountID, &v.Amount, &v.ReceivedAt, &v.TransactionID); e != nil {
			return e
		}
		v.IsReceived = v.ReceivedAt != nil
		f.Recipients = append(f.Recipients, v)
	}
	return rows.Err()
}

// Fund details and contributors are public in the retained controller. The
// random fund ID is a locator, never authorization to claim or contribute.
func (r *Repository) Fund(ctx context.Context, actor, id string) (accounts.Fund, error) {
	if !accounts.ValidID(id) {
		return accounts.Fund{}, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return accounts.Fund{}, e
	}
	defer tx.Rollback()
	f, e := fundAt(ctx, tx, id, false)
	if e != nil {
		return f, e
	}
	if e = hydrateFund(ctx, tx, &f); e != nil {
		return f, e
	}
	return f, tx.Commit()
}

// ValidateChatFund runs inside the caller's message transaction. Only a fund's
// creator may compose its public locator into a chat message.
func (r *Repository) ValidateChatFund(ctx context.Context, tx *sql.Tx, actor, id string) error {
	if r == nil || tx == nil || !accounts.ValidID(actor) || !accounts.ValidID(id) {
		return accounts.ErrInvalid
	}
	var owner string
	e := tx.QueryRowContext(ctx, `SELECT creator_account_id::text FROM wallet_v1.funds WHERE id=$1`, id).Scan(&owner)
	if errors.Is(e, sql.ErrNoRows) {
		return accounts.ErrNotFound
	}
	if e != nil {
		return e
	}
	if owner != actor {
		return accounts.ErrForbidden
	}
	return nil
}
func (r *Repository) Funds(ctx context.Context, actor string, offset, take int, status *int) ([]accounts.Fund, int, error) {
	if !accounts.ValidID(actor) || offset < 0 || take < 0 || take > 100 || status != nil && (*status < 0 || *status > 4) {
		return nil, 0, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	var total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM wallet_v1.funds WHERE creator_account_id=$1 AND($2::integer IS NULL OR status=$2)`, actor, status).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fundCols+` FROM wallet_v1.funds WHERE creator_account_id=$1 AND($2::integer IS NULL OR status=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, actor, status, take, offset)
	if e != nil {
		return nil, 0, e
	}
	fs := []accounts.Fund{}
	for rows.Next() {
		f, x := scanFund(rows)
		if x != nil {
			rows.Close()
			return nil, 0, x
		}
		fs = append(fs, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range fs {
		if e = hydrateFund(ctx, tx, &fs[i]); e != nil {
			return nil, 0, e
		}
	}
	return fs, total, tx.Commit()
}
func (r *Repository) fundWrite(ctx context.Context, tx *sql.Tx, actor, purpose, binding string) error {
	if r.authority == nil {
		return accounts.ErrUnavailable
	}
	if e := r.writeAuthority(ctx, tx, accounts.Owner{AccountID: actor}, actor, accounts.PermissionFunds); e != nil {
		return e
	}
	if purpose != "" {
		if r.payments == nil {
			return accounts.ErrUnavailable
		}
		return r.payments.LockPayment(ctx, r.db, tx, actor, purpose, binding)
	}
	return nil
}
func (r *Repository) CreateFund(ctx context.Context, c accounts.FundCommand) (accounts.Fund, error) {
	var f accounts.Fund
	if !accounts.ValidID(c.ActorID) || !accounts.ValidID(c.WalletID) || !paymentOperation(accounts.PaymentFundCreate, c.Fingerprint) || len(c.IdempotencyKey) > 128 {
		return f, accounts.ErrInvalid
	}
	if c.Fingerprint != c.Binding() {
		return f, accounts.ErrPIN
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return f, e
	}
	defer tx.Rollback()
	if r.authority == nil {
		return f, accounts.ErrUnavailable
	}
	if _, e = r.authority.Preflight(ctx, r.db, tx, c.ActorID, accounts.PermissionFunds); e != nil {
		return f, e
	}
	// Invitations create account foreign keys. Lock the complete reference set in
	// UUID order before upgrading the actor authority row, preventing opposing
	// creators from acquiring each other's account foreign keys in reverse order.
	ids := append([]string{c.ActorID}, c.Request.RecipientAccountIDs...)
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id::text=ANY($1::text[]) ORDER BY id FOR UPDATE`, ids)
	if e != nil {
		return f, e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return f, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return f, e
	}
	if e = r.fundWrite(ctx, tx, c.ActorID, accounts.PaymentFundCreate, c.Fingerprint); e != nil {
		return f, e
	}
	if c.IdempotencyKey != "" {
		if e = lock(ctx, tx, "fund-idempotency:"+c.ActorID+":"+c.IdempotencyKey); e != nil {
			return f, e
		}
		var id, hash string
		e = tx.QueryRowContext(ctx, `SELECT id::text,fingerprint FROM wallet_v1.funds WHERE creator_account_id=$1 AND idempotency_key=$2`, c.ActorID, c.IdempotencyKey).Scan(&id, &hash)
		if e == nil {
			if hash != c.Fingerprint {
				return f, accounts.ErrIdempotency
			}
			f, e = fundAt(ctx, tx, id, false)
			if e != nil {
				return f, e
			}
			if e = hydrateFund(ctx, tx, &f); e != nil {
				return f, e
			}
			if e = r.payments.CheckPayment(ctx, r.db, tx, c.ActorID, accounts.PaymentFundCreate, c.Fingerprint); e != nil {
				return f, e
			}
			return f, tx.Commit()
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return f, e
		}
	}
	w, e := wallet(ctx, tx, c.WalletID, true)
	if e != nil {
		return f, e
	}
	if w.AccountID == nil || *w.AccountID != c.ActorID {
		return f, accounts.ErrForbidden
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	q, e := accounts.NormalizeFund(c.Request, now)
	if e != nil {
		return f, e
	}
	id, e := uuid()
	if e != nil {
		return f, e
	}
	f, e = scanFund(tx.QueryRowContext(ctx, `INSERT INTO wallet_v1.funds(id,creator_account_id,beneficiary_wallet_id,currency,total_amount,amount_of_splits,split_type,message,is_open,is_raising,target_amount,contribution_type,contribution_amount,deadline_at,expires_at,created_at,updated_at,idempotency_key,fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16,$17,$18) RETURNING `+fundCols, id, c.ActorID, c.WalletID, q.Currency, q.TotalAmount, q.AmountOfSplits, q.SplitType, q.Message, q.IsOpen, q.IsRaising, q.TargetAmount, q.ContributionType, q.ContributionAmount, q.DeadlineAt, now.Add(time.Duration(*q.ExpirationHours)*time.Hour), now, nullable(c.IdempotencyKey), c.Fingerprint))
	if e != nil {
		return f, e
	}
	for _, actor := range q.RecipientAccountIDs {
		if e = insertParticipant(ctx, tx, f.ID, actor, true); e != nil {
			return f, e
		}
	}
	if !f.IsRaising {
		if _, e = reserveFund(ctx, tx, f, c.ActorID, c.WalletID, q.TotalAmount, nil, now); e != nil {
			return f, e
		}
	}
	if e = hydrateFund(ctx, tx, &f); e != nil {
		return f, e
	}
	if e = event(ctx, tx, "wallet.fund.created", f.ID, fundEvent(f), now); e != nil {
		return f, e
	}
	if e = r.payments.CheckPayment(ctx, r.db, tx, c.ActorID, accounts.PaymentFundCreate, c.Fingerprint); e != nil {
		return f, e
	}
	return f, tx.Commit()
}
func insertParticipant(ctx context.Context, tx *sql.Tx, fund, actor string, invited bool) error {
	id, e := uuid()
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.fund_participants(id,fund_id,account_id,is_invited)VALUES($1,$2,$3,$4)ON CONFLICT(fund_id,account_id)DO NOTHING`, id, fund, actor, invited)
	return e
}

type fundReservation struct {
	ID, Actor, Wallet string
	Amount, Remaining accounts.Amount
	Receipt           *string
}

func reservations(ctx context.Context, tx *sql.Tx, id string) ([]fundReservation, error) {
	rows, e := tx.QueryContext(ctx, `SELECT r.id::text,r.account_id::text,r.payer_wallet_id::text,r.amount::text,(r.amount-COALESCE((SELECT sum(t.amount) FROM wallet_v1.fund_allocations a JOIN wallet_v1.transactions t ON t.id=a.transaction_id WHERE a.reservation_id=r.id),0))::text,r.receipt_transaction_id::text FROM wallet_v1.fund_reservations r WHERE fund_id=$1 ORDER BY r.id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []fundReservation{}
	for rows.Next() {
		var r fundReservation
		if e = rows.Scan(&r.ID, &r.Actor, &r.Wallet, &r.Amount, &r.Remaining, &r.Receipt); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func reserveFund(ctx context.Context, tx *sql.Tx, f accounts.Fund, actor, payer string, amount accounts.Amount, receipt *string, now time.Time) (string, error) {
	if e := ensurePocket(ctx, tx, payer, f.Currency, now); e != nil {
		return "", e
	}
	if e := changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET amount=amount-$3,held_amount=held_amount+$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND amount >= $3`, payer, f.Currency, amount, now)); e != nil {
		return "", e
	}
	id, e := uuid()
	if e != nil {
		return "", e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.fund_reservations(id,fund_id,account_id,payer_wallet_id,amount,created_at,receipt_transaction_id)VALUES($1,$2,$3,$4,$5,$6,$7)`, id, f.ID, actor, payer, amount, now, receipt)
	if e != nil {
		return "", e
	}
	return id, pocketEvent(ctx, tx, payer, f.Currency, now)
}
func fundDue(f accounts.Fund, now time.Time) bool {
	return !now.Before(f.ExpiredAt) || f.DeadlineAt != nil && !now.Before(*f.DeadlineAt)
}
func fundTerminal(f accounts.Fund) bool { return f.Status >= accounts.FundCompleted }
func fundWalletIDs(rs []fundReservation, extra ...string) []string {
	ids := append([]string(nil), extra...)
	for _, r := range rs {
		ids = append(ids, r.Wallet)
	}
	return ids
}

// newFundReceipt is the only constructor for managed fund journal entries.
// A self transaction is valid solely as a typed final refund of a known hold.
func newFundReceipt(ctx context.Context, tx *sql.Tx, f accounts.Fund, actor, payer, payee, kind string, amount accounts.Amount, now time.Time) (accounts.Transaction, error) {
	var t accounts.Transaction
	if payer == payee && kind != "refund" {
		return t, accounts.ErrInvalid
	}
	id, e := uuid()
	if e != nil {
		return t, e
	}
	status := accounts.Confirmed
	var frozen, confirmed *time.Time
	if kind == "hold" {
		status = accounts.Frozen
		frozen = &now
	} else if kind == "refund" {
		status = accounts.Refunded
	} else {
		confirmed = &now
	}
	t = accounts.Transaction{ID: id, Type: 0, PayerWalletID: payer, PayeeWalletID: payee, Currency: f.Currency, Amount: amount, Status: status, IsFrozen: kind == "hold", FrozenAt: frozen, ConfirmedAt: confirmed, CreatedAt: now, UpdatedAt: now, Remarks: f.Message}
	hash := sha256.Sum256([]byte(f.ID + ":" + id + ":" + kind + ":" + amount.String()))
	_, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,remarks,status,is_frozen,require_confirmation,frozen_at,confirmed_at,created_at,updated_at,fingerprint,purpose,managed_by,fund_kind)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,false,$10,$11,$12,$12,$13,'fund',$14,$15)`, id, actor, payer, payee, f.Currency, amount, f.Message, status, t.IsFrozen, frozen, confirmed, now, hex.EncodeToString(hash[:]), f.ID, kind)
	return t, e
}
func allocateFund(ctx context.Context, tx *sql.Tx, f accounts.Fund, r fundReservation, payee, kind string, amount accounts.Amount, now time.Time) (accounts.Transaction, error) {
	var t accounts.Transaction
	if amount.Sign() <= 0 || amount.Cmp(r.Remaining) > 0 {
		return t, accounts.ErrAmount
	}
	var e error
	if r.Receipt != nil {
		if amount.Cmp(r.Remaining) != 0 || amount.Cmp(r.Amount) != 0 || kind == "claim" {
			return t, accounts.ErrConflict
		}
		t, e = scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1 AND purpose='fund' AND managed_by=$2 AND fund_kind='hold' AND status=1 FOR UPDATE`, *r.Receipt, f.ID))
		if e != nil {
			return t, e
		}
		t.Status = accounts.Confirmed
		t.IsFrozen = false
		t.UpdatedAt = now
		t.ConfirmedAt = &now
		if kind == "refund" {
			t.Status = accounts.Refunded
			t.ConfirmedAt = nil
		}
		if e = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.transactions SET status=$2,is_frozen=false,confirmed_at=$3,updated_at=$4,fund_kind=$5 WHERE id=$1 AND status=1`, t.ID, t.Status, t.ConfirmedAt, now, kind)); e != nil {
			return t, e
		}
	} else {
		t, e = newFundReceipt(ctx, tx, f, r.Actor, r.Wallet, payee, kind, amount, now)
		if e != nil {
			return t, e
		}
	}
	if e = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.pockets SET held_amount=held_amount-$3,updated_at=$4 WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL AND held_amount >= $3`, r.Wallet, f.Currency, amount, now)); e != nil {
		return t, e
	}
	if kind == "refund" {
		payee = r.Wallet
	}
	if e = credit(ctx, tx, payee, f.Currency, amount, now); e != nil {
		return t, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.fund_allocations(reservation_id,transaction_id,kind)VALUES($1,$2,$3)`, r.ID, t.ID, kind); e != nil {
		return t, e
	}
	eventType := "wallet.transaction.confirmed"
	if kind == "refund" {
		eventType = "wallet.transaction.refunded"
	}
	if e = event(ctx, tx, eventType, t.ID, t, now); e != nil {
		return t, e
	}
	if e = pocketEvent(ctx, tx, r.Wallet, f.Currency, now); e != nil {
		return t, e
	}
	if payee != r.Wallet {
		e = pocketEvent(ctx, tx, payee, f.Currency, now)
	}
	return t, e
}
func finishFund(ctx context.Context, tx *sql.Tx, f *accounts.Fund, rs []fundReservation, status int, now time.Time) error {
	kind := "refund"
	if status == accounts.FundCompleted {
		kind = "payout"
	}
	for _, r := range rs {
		if r.Remaining.Sign() == 0 {
			continue
		}
		payee := r.Wallet
		if kind == "payout" {
			payee = f.BeneficiaryWalletID
		}
		if _, e := allocateFund(ctx, tx, *f, r, payee, kind, r.Remaining, now); e != nil {
			return e
		}
	}
	return setFundStatus(ctx, tx, f, status, now)
}
func setFundStatus(ctx context.Context, tx *sql.Tx, f *accounts.Fund, status int, now time.Time) error {
	if _, e := tx.ExecContext(ctx, `UPDATE wallet_v1.funds SET status=$2,updated_at=$3 WHERE id=$1`, f.ID, status, now); e != nil {
		return e
	}
	f.Status = status
	f.UpdatedAt = now
	if e := hydrateFund(ctx, tx, f); e != nil {
		return e
	}
	return event(ctx, tx, "wallet.fund.updated", f.ID, fundEvent(*f), now)
}
func (r *Repository) ReceiveFund(ctx context.Context, actor, id string) (accounts.Transaction, error) {
	var out accounts.Transaction
	if !accounts.ValidID(actor) || !accounts.ValidID(id) {
		return out, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = r.fundWrite(ctx, tx, actor, "", ""); e != nil {
		return out, e
	}
	f, e := fundAt(ctx, tx, id, true)
	if e != nil {
		return out, e
	}
	if f.IsRaising {
		return out, accounts.ErrInvalid
	}
	var invited bool
	var received *time.Time
	var receipt *string
	e = tx.QueryRowContext(ctx, `SELECT is_invited,received_at,transaction_id::text FROM wallet_v1.fund_participants WHERE fund_id=$1 AND account_id=$2`, id, actor).Scan(&invited, &received, &receipt)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if receipt != nil {
		out, e = scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1`, *receipt))
		if e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	if !f.IsOpen && !invited {
		return out, accounts.ErrForbidden
	}
	if fundTerminal(f) || fundDue(f, r.now()) {
		return out, accounts.ErrConflict
	}
	if e = lock(ctx, tx, "owner:account:"+actor); e != nil {
		return out, e
	}
	payee, e := scanWallet(tx.QueryRowContext(ctx, `SELECT `+walletCols+` FROM wallet_v1.wallets WHERE account_id=$1 AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, actor))
	if e != nil {
		return out, e
	}
	rs, e := reservations(ctx, tx, id)
	if e != nil {
		return out, e
	}
	if len(rs) != 1 {
		return out, accounts.ErrConflict
	}
	if payee.ID == rs[0].Wallet {
		return out, accounts.ErrInvalid
	}
	if _, e = lockWallets(ctx, tx, rs[0].Wallet, payee.ID); e != nil {
		return out, e
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	if fundDue(f, now) {
		return out, accounts.ErrConflict
	}
	var count, total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*)FILTER(WHERE received_at IS NOT NULL),count(*) FROM wallet_v1.fund_participants WHERE fund_id=$1`, id).Scan(&count, &total); e != nil {
		return out, e
	}
	slots := f.AmountOfSplits - count
	if !f.IsOpen {
		slots = total - count
	}
	amount, e := accounts.FundShare(rs[0].Remaining, slots, f.SplitType, nil)
	if e != nil {
		return out, e
	}
	out, e = allocateFund(ctx, tx, f, rs[0], payee.ID, "claim", amount, now)
	if e != nil {
		return out, e
	}
	if e = insertParticipant(ctx, tx, id, actor, false); e != nil {
		return out, e
	}
	if e = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.fund_participants SET received_at=$3,transaction_id=$4 WHERE fund_id=$1 AND account_id=$2 AND received_at IS NULL`, id, actor, now, out.ID)); e != nil {
		return out, e
	}
	status := accounts.FundPartial
	if slots == 1 {
		status = accounts.FundCompleted
	}
	if e = setFundStatus(ctx, tx, &f, status, now); e != nil {
		return out, e
	}
	if e = r.fundWrite(ctx, tx, actor, "", ""); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) ContributeFund(ctx context.Context, c accounts.FundContributionCommand) (accounts.Transaction, error) {
	var out accounts.Transaction
	if !accounts.ValidID(c.ActorID) || !accounts.ValidID(c.FundID) || !accounts.ValidID(c.WalletID) || c.Amount.Sign() <= 0 || !paymentOperation(accounts.PaymentFundContribute, c.Fingerprint) {
		return out, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = r.fundWrite(ctx, tx, c.ActorID, accounts.PaymentFundContribute, c.Fingerprint); e != nil {
		return out, e
	}
	f, e := fundAt(ctx, tx, c.FundID, true)
	if e != nil {
		return out, e
	}
	if !f.IsRaising {
		return out, accounts.ErrInvalid
	}
	if c.Fingerprint != c.Binding(f) {
		return out, accounts.ErrPIN
	}
	var invited bool
	var receipt *string
	e = tx.QueryRowContext(ctx, `SELECT is_invited,transaction_id::text FROM wallet_v1.fund_participants WHERE fund_id=$1 AND account_id=$2`, f.ID, c.ActorID).Scan(&invited, &receipt)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if receipt != nil {
		out, e = scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1`, *receipt))
		if e != nil {
			return out, e
		}
		if out.Amount.Cmp(c.Amount) != 0 || out.PayerWalletID != c.WalletID {
			return out, accounts.ErrIdempotency
		}
		if e = r.payments.CheckPayment(ctx, r.db, tx, c.ActorID, accounts.PaymentFundContribute, c.Fingerprint); e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	if !f.IsOpen && !invited {
		return out, accounts.ErrForbidden
	}
	if fundTerminal(f) || fundDue(f, r.now()) {
		return out, accounts.ErrConflict
	}
	if f.ContributionType == accounts.ContributionFixed && c.Amount.Cmp(f.ContributionAmount) != 0 {
		return out, accounts.ErrAmount
	}
	rs, e := reservations(ctx, tx, f.ID)
	if e != nil {
		return out, e
	}
	if len(rs) >= f.AmountOfSplits {
		return out, accounts.ErrQuota
	}
	raised := c.Amount
	for _, v := range rs {
		raised, e = raised.Add(v.Amount)
		if e != nil {
			return out, e
		}
	}
	if raised.Cmp(f.TargetAmount) > 0 {
		return out, accounts.ErrAmount
	}
	if c.WalletID == f.BeneficiaryWalletID {
		return out, accounts.ErrInvalid
	}
	ws, e := lockWallets(ctx, tx, fundWalletIDs(rs, c.WalletID, f.BeneficiaryWalletID)...)
	if e != nil {
		return out, e
	}
	if ws[c.WalletID].AccountID == nil || *ws[c.WalletID].AccountID != c.ActorID || ws[f.BeneficiaryWalletID].AccountID == nil || *ws[f.BeneficiaryWalletID].AccountID != f.CreatorAccountID {
		return out, accounts.ErrForbidden
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	if fundDue(f, now) {
		return out, accounts.ErrConflict
	}
	out, e = newFundReceipt(ctx, tx, f, c.ActorID, c.WalletID, f.BeneficiaryWalletID, "hold", c.Amount, now)
	if e != nil {
		return out, e
	}
	reservation, e := reserveFund(ctx, tx, f, c.ActorID, c.WalletID, c.Amount, &out.ID, now)
	if e != nil {
		return out, e
	}
	if e = insertParticipant(ctx, tx, f.ID, c.ActorID, false); e != nil {
		return out, e
	}
	if e = changed(tx.ExecContext(ctx, `UPDATE wallet_v1.fund_participants SET received_at=$3,transaction_id=$4,reservation_id=$5 WHERE fund_id=$1 AND account_id=$2 AND received_at IS NULL`, f.ID, c.ActorID, now, out.ID, reservation)); e != nil {
		return out, e
	}
	if e = event(ctx, tx, "wallet.transaction.created", out.ID, out, now); e != nil {
		return out, e
	}
	if raised.Cmp(f.TargetAmount) == 0 {
		rs = append(rs, fundReservation{ID: reservation, Actor: c.ActorID, Wallet: c.WalletID, Amount: c.Amount, Remaining: c.Amount, Receipt: &out.ID})
		if e = finishFund(ctx, tx, &f, rs, accounts.FundCompleted, now); e != nil {
			return out, e
		}
		out, e = scanTransaction(tx.QueryRowContext(ctx, `SELECT `+transactionCols+` FROM wallet_v1.transactions WHERE id=$1`, out.ID))
		if e != nil {
			return out, e
		}
	} else {
		if e = setFundStatus(ctx, tx, &f, accounts.FundPartial, now); e != nil {
			return out, e
		}
	}
	if e = r.payments.CheckPayment(ctx, r.db, tx, c.ActorID, accounts.PaymentFundContribute, c.Fingerprint); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) CancelFund(ctx context.Context, actor, id string) (accounts.Fund, error) {
	var f accounts.Fund
	if !accounts.ValidID(actor) || !accounts.ValidID(id) {
		return f, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return f, e
	}
	defer tx.Rollback()
	if e = r.fundWrite(ctx, tx, actor, "", ""); e != nil {
		return f, e
	}
	f, e = fundAt(ctx, tx, id, true)
	if e != nil {
		return f, e
	}
	if f.CreatorAccountID != actor {
		return f, accounts.ErrForbidden
	}
	if fundTerminal(f) {
		if f.Status == accounts.FundCompleted {
			return f, accounts.ErrConflict
		}
		if e = hydrateFund(ctx, tx, &f); e != nil {
			return f, e
		}
		return f, tx.Commit()
	}
	rs, e := reservations(ctx, tx, id)
	if e != nil {
		return f, e
	}
	if _, e = lockWallets(ctx, tx, fundWalletIDs(rs)...); e != nil {
		return f, e
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	status := accounts.FundRefunded
	if fundDue(f, now) {
		status = accounts.FundExpired
	}
	if e = finishFund(ctx, tx, &f, rs, status, now); e != nil {
		return f, e
	}
	if e = r.fundWrite(ctx, tx, actor, "", ""); e != nil {
		return f, e
	}
	return f, tx.Commit()
}

// ProcessFundsDue serializes with claims, final contributions and cancellation.
// Expiry releases precisely the unallocated reservations; terminal funds cannot
// be refunded again. It may run after restart and has no provider dependency.
func (r *Repository) ProcessFundsDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, accounts.ErrInvalid
	}
	if r == nil || r.db == nil {
		return 0, accounts.ErrUnavailable
	}
	rows, e := r.db.QueryContext(ctx, `SELECT id::text FROM wallet_v1.funds WHERE status IN(0,1) AND LEAST(expires_at,deadline_at)<=$1 ORDER BY LEAST(expires_at,deadline_at),id LIMIT $2`, r.now().UTC(), limit)
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	for _, id := range ids {
		done, x := r.expireFund(ctx, id)
		if x != nil {
			return count, x
		}
		if done {
			count++
		}
	}
	return count, nil
}
func (r *Repository) expireFund(ctx context.Context, id string) (bool, error) {
	tx, e := r.begin(ctx, false)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	f, e := fundAt(ctx, tx, id, true)
	if e != nil {
		return false, e
	}
	if fundTerminal(f) || !fundDue(f, r.now()) {
		return false, tx.Commit()
	}
	rs, e := reservations(ctx, tx, id)
	if e != nil {
		return false, e
	}
	if _, e = lockWallets(ctx, tx, fundWalletIDs(rs)...); e != nil {
		return false, e
	}
	if e = finishFund(ctx, tx, &f, rs, accounts.FundExpired, r.now().UTC().Truncate(time.Microsecond)); e != nil {
		return false, e
	}
	return true, tx.Commit()
}
func (r *Repository) FundsReady(ctx context.Context) error {
	if r == nil || r.db == nil {
		return accounts.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,beneficiary_wallet_id,status FROM wallet_v1.funds LIMIT 0;SELECT receipt_transaction_id FROM wallet_v1.fund_reservations LIMIT 0;SELECT kind FROM wallet_v1.fund_allocations LIMIT 0;SELECT reservation_id FROM wallet_v1.fund_participants LIMIT 0;SELECT fund_kind FROM wallet_v1.transactions LIMIT 0`)
	return e
}

// Outbox invalidations remain below the notification payload limit even for
// 512 participants. Consumers fetch the authoritative public fund projection.
func fundEvent(f accounts.Fund) any {
	return struct {
		ID        string          `json:"id"`
		Currency  string          `json:"currency"`
		Message   *string         `json:"message"`
		Status    int             `json:"status"`
		Raising   bool            `json:"is_raising"`
		Raised    accounts.Amount `json:"raised_amount"`
		Remaining accounts.Amount `json:"remaining_amount"`
		Target    accounts.Amount `json:"target_amount"`
	}{f.ID, f.Currency, f.Message, f.Status, f.IsRaising, f.RaisedAmount, f.RemainingAmount, f.TargetAmount}
}
