// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

//go:embed transfer_requests_schema.sql
var TransferRequestsSchema string

const requestFields = `r.id::text,r.status,r.currency,r.amount::text,r.remark,r.is_frozen,r.require_confirmation,r.expires_at,r.fulfilled_at,r.payee_wallet_id::text,w.public_id,r.transaction_id::text`

func scanRequest(row interface{ Scan(...any) error }) (v accounts.TransferRequestRecord, e error) {
	e = row.Scan(&v.ID, &v.Status, &v.Currency, &v.Amount, &v.Remark, &v.Freeze, &v.RequireConfirmation, &v.ExpiresAt, &v.FulfilledAt, &v.PayeeWalletID, &v.PayeePublicID, &v.TransactionID)
	return v, mapErr(e)
}

const requestPublicWallet = `w.deleted_at IS NULL AND w.public_id IS NOT NULL AND ((w.account_id IS NOT NULL AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=w.account_id AND NOT a.disabled)) OR(w.realm_id IS NOT NULL AND EXISTS(SELECT 1 FROM owned_realms realm WHERE realm.id=w.realm_id AND realm.deleted_at IS NULL)))`

func (r *Repository) TransferRequestsReady(ctx context.Context) error {
	if r == nil || r.db == nil {
		return accounts.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,transaction_id FROM wallet_v1.transfer_requests LIMIT 0`)
	return e
}
func (r *Repository) CreateTransferRequest(ctx context.Context, actor string, owner accounts.Owner, wid string, q accounts.CreateTransferRequest) (out accounts.TransferRequestRecord, e error) {
	if !accounts.ValidID(actor) || !accounts.ValidID(wid) || !owner.Valid() {
		return out, accounts.ErrInvalid
	}
	if e = accounts.ValidateTransferRequest(q); e != nil {
		return
	}
	if r == nil || r.authority == nil {
		return out, accounts.ErrUnavailable
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if e = r.writeAuthority(ctx, tx, owner, actor, accounts.PermissionTransferRequests); e != nil {
		return
	}
	wallets, e := lockWallets(ctx, tx, wid)
	if e != nil {
		return
	}
	w := wallets[wid]
	if w.Owner() != owner {
		return out, accounts.ErrForbidden
	}
	if w.PublicID == nil || *w.PublicID == "" {
		return out, accounts.ErrInvalid
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*)FROM wallet_v1.transfer_requests WHERE payee_wallet_id=$1::uuid AND status=0 AND expires_at>$2::timestamptz`, wid, now).Scan(&count); e != nil {
		return
	}
	if count >= 1000 {
		return out, accounts.ErrQuota
	}
	id, e := uuid()
	if e != nil {
		return
	}
	out = accounts.TransferRequestRecord{ID: id, Status: accounts.RequestActive, Currency: q.Currency, Amount: q.Amount, Remark: q.Remark, Freeze: q.Freeze, RequireConfirmation: q.RequireConfirmation, ExpiresAt: now.Add(time.Duration(q.ExpirationHours) * time.Hour), PayeeWalletID: wid, PayeePublicID: w.PublicID}
	_, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.transfer_requests(id,creator_account_id,payee_wallet_id,currency,amount,remark,is_frozen,require_confirmation,created_at,expires_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,$5::numeric,$6::text,$7::boolean,$8::boolean,$9::timestamptz,$10::timestamptz)`, id, actor, wid, q.Currency, q.Amount, q.Remark, q.Freeze, q.RequireConfirmation, now, out.ExpiresAt)
	if e != nil {
		return
	}
	if e = r.writeAuthority(ctx, tx, owner, actor, accounts.PermissionTransferRequests); e != nil {
		return
	}
	e = tx.Commit()
	return
}
func (r *Repository) PublicTransferRequest(ctx context.Context, id string) (accounts.TransferRequestRecord, error) {
	if r == nil || r.db == nil {
		return accounts.TransferRequestRecord{}, accounts.ErrUnavailable
	}
	if !accounts.ValidID(id) {
		return accounts.TransferRequestRecord{}, accounts.ErrNotFound
	}
	return scanRequest(r.db.QueryRowContext(ctx, `SELECT `+requestFields+` FROM wallet_v1.transfer_requests r JOIN wallet_v1.wallets w ON w.id=r.payee_wallet_id WHERE r.id=$1::uuid AND r.status=0 AND r.expires_at>$2::timestamptz AND `+requestPublicWallet, id, r.now().UTC()))
}
func (r *Repository) ResolveTransferRequest(ctx context.Context, actor, id string) (accounts.TransferRequestRecord, error) {
	if r == nil || r.db == nil || r.authority == nil {
		return accounts.TransferRequestRecord{}, accounts.ErrUnavailable
	}
	if !accounts.ValidID(actor) || !accounts.ValidID(id) {
		return accounts.TransferRequestRecord{}, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return accounts.TransferRequestRecord{}, e
	}
	defer tx.Rollback()
	if e = r.writeAuthority(ctx, tx, accounts.Owner{AccountID: actor}, actor, accounts.PermissionTransactions); e != nil {
		return accounts.TransferRequestRecord{}, e
	}
	v, e := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestFields+` FROM wallet_v1.transfer_requests r JOIN wallet_v1.wallets w ON w.id=r.payee_wallet_id WHERE r.id=$1::uuid AND ((r.status=0 AND r.expires_at>$2::timestamptz AND `+requestPublicWallet+`)OR(r.status=1 AND EXISTS(SELECT 1 FROM wallet_v1.transactions t WHERE t.id=r.transaction_id AND t.actor_id=$3::uuid AND t.deleted_at IS NULL)))`, id, r.now().UTC(), actor))
	return v, e
}
func (r *Repository) lockTransferRequest(ctx context.Context, tx *sql.Tx, c accounts.TransferCommand, replay string) error {
	if c.TransferRequestID == "" {
		return nil
	}
	if !accounts.ValidID(c.TransferRequestID) {
		return accounts.ErrInvalid
	}
	v, e := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestFields+` FROM wallet_v1.transfer_requests r JOIN wallet_v1.wallets w ON w.id=r.payee_wallet_id WHERE r.id=$1::uuid FOR UPDATE OF r`, c.TransferRequestID))
	if e != nil {
		return e
	}
	if replay != "" {
		if v.Status != accounts.RequestFulfilled || v.TransactionID == nil || *v.TransactionID != replay {
			return accounts.ErrIdempotency
		}
		return nil
	}
	if v.Status != accounts.RequestActive || !v.ExpiresAt.After(r.now().UTC()) {
		return accounts.ErrConflict
	}
	if v.PayeeWalletID != c.PayeeWalletID || v.Currency != c.Currency || v.Amount.Cmp(c.Amount) != 0 || v.Freeze != c.Freeze || v.RequireConfirmation != c.RequireConfirmation || !sameRemark(v.Remark, c.Remark) {
		return accounts.ErrConflict
	}
	return nil
}
func sameRemark(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func (r *Repository) fulfillTransferRequest(ctx context.Context, tx *sql.Tx, c accounts.TransferCommand, transaction string) error {
	if c.TransferRequestID == "" {
		return nil
	}
	now := r.now().UTC()
	result, e := tx.ExecContext(ctx, `UPDATE wallet_v1.transfer_requests r SET status=1,fulfilled_at=$3::timestamptz,transaction_id=$2::uuid FROM wallet_v1.wallets w WHERE r.id=$1::uuid AND r.payee_wallet_id=w.id AND r.status=0 AND r.expires_at>$3::timestamptz AND `+requestPublicWallet, c.TransferRequestID, transaction, now)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return accounts.ErrConflict
	}
	return nil
}

// Secondary recipient state is locked NOWAIT after payer authority. Opposing
// transfers may retry rather than deadlock or spend against stale recipient state.
func (r *Repository) lockRequestPayee(ctx context.Context, tx *sql.Tx, wid string) error {
	var account, realm sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT account_id::text,realm_id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND deleted_at IS NULL AND public_id IS NOT NULL`, wid).Scan(&account, &realm); e != nil {
		return mapErr(e)
	}
	var valid bool
	var e error
	if account.Valid {
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR SHARE NOWAIT`, account.String).Scan(&valid)
	} else if realm.Valid {
		e = tx.QueryRowContext(ctx, `SELECT deleted_at IS NULL FROM owned_realms WHERE id=$1::uuid FOR SHARE NOWAIT`, realm.String).Scan(&valid)
	} else {
		return accounts.ErrNotFound
	}
	if e != nil {
		return transferRequestError(e)
	}
	if !valid {
		return accounts.ErrNotFound
	}
	return nil
}

func transferRequestError(e error) error {
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "55P03" {
		return accounts.ErrConflict
	}
	return mapErr(e)
}
