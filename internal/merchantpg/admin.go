// SPDX-License-Identifier: AGPL-3.0-only
package merchantpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"solsynth.local/go-migration/ports/wallet/merchants"
)

func merchantUUID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func scopedWalletKey(scope, key string) string {
	sum := sha256.Sum256([]byte(scope + ":" + key))
	return hex.EncodeToString(sum[:])
}
func commerceEvent(ctx context.Context, tx *sql.Tx, kind, id string, payload any, now time.Time) error {
	eid, e := merchantUUID()
	if e != nil {
		return e
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.outbox(event_id,event_type,aggregate_id,payload,created_at)VALUES($1,$2,$3::uuid,$4::jsonb,$5)`, eid, "commerce."+kind, id, raw, now)
	return e
}

type maintenanceStore interface {
	OperatorSubscriptionMaintenanceFailures(context.Context, string, string, int) ([]commercepg.MaintenanceQuarantine, error)
	OperatorRetrySubscriptionMaintenance(context.Context, string, string) error
	OperatorProcessDue(context.Context, string, int) (int, error)
}

func (r *Repository) WithMaintenance(v maintenanceStore) *Repository { r.maintenance = v; return r }
func (r *Repository) WithWalletProductCatalog(v json.RawMessage) *Repository {
	r.walletProduct = append(r.walletProduct[:0], v...)
	return r
}
func (r *Repository) WithWalletProductTreasury(walletID string) *Repository {
	r.walletProductTreasury = walletID
	return r
}

const adminTransactionCols = `CASE WHEN purpose='commerce' THEN 1 ELSE 0 END,id::text,payer_wallet_id::text,payee_wallet_id::text,currency,amount::text,remarks,status,is_frozen,require_confirmation,frozen_at,expires_at,confirmed_at,confirmation_received_at,created_at,updated_at,deleted_at`

func scanAdminTransaction(row interface{ Scan(...any) error }) (wallet.Transaction, error) {
	var v wallet.Transaction
	err := row.Scan(&v.Type, &v.ID, &v.PayerWalletID, &v.PayeeWalletID, &v.Currency, &v.Amount, &v.Remarks, &v.Status, &v.IsFrozen, &v.RequireConfirmation, &v.FrozenAt, &v.ExpiresAt, &v.ConfirmedAt, &v.ConfirmationReceivedAt, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	return v, mapped(err)
}

func adminBounds(q merchants.AdminQuery) (merchants.AdminQuery, error) {
	if q.Take == 0 {
		q.Take = 50
	}
	if q.Take < 1 || q.Take > 200 || q.Offset < 0 || q.Offset > 100000 {
		return q, merchants.ErrInvalid
	}
	for _, id := range []string{q.WalletID, q.AccountID} {
		if id != "" && !authority.ValidID(id) {
			return q, merchants.ErrInvalid
		}
	}
	if q.Status != nil && (*q.Status < 0 || *q.Status > 4) || q.Type != nil && (*q.Type < 0 || *q.Type > 1) {
		return q, merchants.ErrInvalid
	}
	return q, nil
}

func (r *Repository) AdminTransactions(ctx context.Context, actor string, q merchants.AdminQuery) (merchants.AdminPage[wallet.Transaction], error) {
	out := merchants.AdminPage[wallet.Transaction]{Items: []wallet.Transaction{}}
	q, err := adminBounds(q)
	if err != nil {
		return out, err
	}
	tx, err := r.begin(ctx, actor, "wallets.transactions.manage")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	where := `t.deleted_at IS NULL AND($1='' OR t.payer_wallet_id::text=$1 OR t.payee_wallet_id::text=$1) AND($2='' OR EXISTS(SELECT 1 FROM wallet_v1.wallets w WHERE w.account_id::text=$2 AND(w.id=t.payer_wallet_id OR w.id=t.payee_wallet_id))) AND($3='' OR t.currency=$3) AND($4::integer IS NULL OR t.status=$4) AND($5::integer IS NULL OR $5=CASE WHEN t.purpose='commerce' THEN 1 ELSE 0 END)`
	args := []any{q.WalletID, q.AccountID, q.Currency, q.Status, q.Type}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM wallet_v1.transactions t WHERE `+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+adminTransactionCols+` FROM wallet_v1.transactions t WHERE `+where+` ORDER BY t.created_at DESC,t.id DESC OFFSET $6 LIMIT $7`, append(args, q.Offset, q.Take)...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		v, e := scanAdminTransaction(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "wallets.transactions.manage"); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (r *Repository) AdminTransaction(ctx context.Context, actor, id string) (wallet.Transaction, error) {
	if !wallet.ValidID(id) {
		return wallet.Transaction{}, merchants.ErrInvalid
	}
	tx, err := r.begin(ctx, actor, "wallets.transactions.manage")
	if err != nil {
		return wallet.Transaction{}, err
	}
	defer tx.Rollback()
	v, err := scanAdminTransaction(tx.QueryRowContext(ctx, `SELECT `+adminTransactionCols+` FROM wallet_v1.transactions WHERE id=$1::uuid AND deleted_at IS NULL`, id))
	if err != nil {
		return v, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "wallets.transactions.manage"); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (r *Repository) AdminOrders(ctx context.Context, actor string, q merchants.AdminQuery) (merchants.AdminPage[merchants.Order], error) {
	out := merchants.AdminPage[merchants.Order]{Items: []merchants.Order{}}
	q, err := adminBounds(q)
	if err != nil {
		return out, err
	}
	tx, err := r.begin(ctx, actor, "orders.view")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	where := `($1='' OR o.payer_wallet_id::text=$1 OR o.payee_wallet_id::text=$1) AND($2='' OR EXISTS(SELECT 1 FROM wallet_v1.wallets w WHERE w.account_id::text=$2 AND(w.id=o.payer_wallet_id OR w.id=o.payee_wallet_id))) AND($3='' OR o.currency=$3) AND($4::integer IS NULL OR o.status=$4) AND($5='' OR COALESCE('developer.app:'||o.app_id::text,'system')=$5) AND($6='' OR o.product_identifier=$6)`
	args := []any{q.WalletID, q.AccountID, q.Currency, q.Status, q.AppIdentifier, q.ProductIdentifier}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM commerce_v1.orders o WHERE `+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.document FROM commerce_v1.orders o WHERE `+where+` ORDER BY o.created_at DESC,o.id DESC OFFSET $7 LIMIT $8`, append(args, q.Offset, q.Take)...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return out, err
		}
		var v merchants.Order
		if err = json.Unmarshal(raw, &v); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.view"); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (r *Repository) AdminOrder(ctx context.Context, actor, id string) (merchants.Order, error) {
	if !wallet.ValidID(id) {
		return merchants.Order{}, merchants.ErrInvalid
	}
	tx, err := r.begin(ctx, actor, "orders.view")
	if err != nil {
		return merchants.Order{}, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.orders WHERE id=$1::uuid`, id).Scan(&raw); err != nil {
		return merchants.Order{}, mapped(err)
	}
	var v merchants.Order
	if err = json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.view"); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (r *Repository) RefundOrder(ctx context.Context, actor, id, key string) (wallet.Transaction, error) {
	if !wallet.ValidID(id) || key == "" || len(key) > 128 {
		return wallet.Transaction{}, merchants.ErrInvalid
	}
	tx, err := r.begin(ctx, actor, "orders.pay")
	if err != nil {
		return wallet.Transaction{}, err
	}
	defer tx.Rollback()
	var raw []byte
	var transactionID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT document,transaction_id::text FROM commerce_v1.orders WHERE id=$1::uuid FOR UPDATE`, id).Scan(&raw, &transactionID)
	if err != nil {
		return wallet.Transaction{}, mapped(err)
	}
	var o c.Order
	if err = json.Unmarshal(raw, &o); err != nil {
		return wallet.Transaction{}, err
	}
	if !transactionID.Valid {
		return wallet.Transaction{}, merchants.ErrConflict
	}
	// Manual settlement owns this row before the managed transaction. Take the
	// same order here so settlement and refund cannot deadlock each other.
	var settlementID string
	var settlementStatus int
	err = tx.QueryRowContext(ctx, `SELECT id::text,status FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid FOR UPDATE`, id).Scan(&settlementID, &settlementStatus)
	settlementFound := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return wallet.Transaction{}, err
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT transaction_id::text FROM commerce_v1.order_reversals WHERE order_id=$1::uuid`, id).Scan(&prior)
	if err == nil {
		out, e := scanAdminTransaction(tx.QueryRowContext(ctx, `SELECT `+adminTransactionCols+` FROM wallet_v1.transactions WHERE id=$1::uuid`, prior))
		if e != nil {
			return out, e
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return wallet.Transaction{}, err
	}
	var out wallet.Transaction
	var status int
	var payer, payee, currency, amount string
	err = tx.QueryRowContext(ctx, `SELECT status,payer_wallet_id::text,payee_wallet_id::text,currency,amount::text FROM wallet_v1.transactions WHERE id=$1::uuid AND purpose='commerce' AND managed_by=$2::uuid FOR UPDATE`, transactionID.String, id).Scan(&status, &payer, &payee, &currency, &amount)
	if err != nil {
		return out, mapped(err)
	}
	now := r.now().UTC()
	if status == int(wallet.Frozen) {
		out, err = walletpg.CommerceSettle(ctx, tx, id, true, now)
	} else if status == int(wallet.Confirmed) {
		parsed, e := wallet.ParseAmount(amount)
		if e != nil {
			return out, e
		}
		out, err = walletpg.FundedReversal(ctx, tx, actor, payee, payer, currency, parsed, o.Remarks, scopedWalletKey("order-refund:"+id, key), now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO commerce_v1.order_reversals(order_id,transaction_id,actor_id,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4)`, id, out.ID, actor, now)
		}
	} else if status == int(wallet.Refunded) {
		out, err = scanAdminTransaction(tx.QueryRowContext(ctx, `SELECT `+adminTransactionCols+` FROM wallet_v1.transactions WHERE id=$1::uuid`, transactionID.String))
		if err != nil {
			return out, err
		}
		// A pending-order refund changes the managed transaction itself to
		// Refunded. Replays must return that durable result without rewriting the
		// order or emitting commerce.order.cancelled again.
		if o.Status != c.Cancelled || settlementFound && settlementStatus != 2 {
			return out, merchants.ErrConflict
		}
		if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); err != nil {
			return wallet.Transaction{}, err
		}
		return out, tx.Commit()
	} else {
		return out, merchants.ErrConflict
	}
	if err != nil {
		return out, err
	}
	o.Status = c.Cancelled
	o.UpdatedAt = now
	raw, err = json.Marshal(o)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE commerce_v1.orders SET status=$2,document=$3::jsonb WHERE id=$1::uuid`, id, o.Status, raw); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE commerce_v1.merchant_settlements SET status=2 WHERE order_id=$1::uuid AND status IN(0,1)`, id); err != nil {
		return out, err
	}
	if err = commerceEvent(ctx, tx, "order.cancelled", id, o, now); err != nil {
		return out, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); err != nil {
		return wallet.Transaction{}, err
	}
	return out, tx.Commit()
}

func (r *Repository) AdminInboundOrders(ctx context.Context, actor string, q merchants.AdminQuery) (merchants.AdminPage[merchants.InboundOrder], error) {
	out := merchants.AdminPage[merchants.InboundOrder]{Items: []merchants.InboundOrder{}}
	q, err := adminBounds(q)
	if err != nil {
		return out, err
	}
	tx, err := r.begin(ctx, actor, "orders.view")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	where := `($1='' OR s.account_id::text=$1)`
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM commerce_v1.provider_receipts p JOIN commerce_v1.subscriptions s ON s.id=p.subscription_id WHERE `+where, q.AccountID).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id::text,p.provider,p.external_id,p.subscription_id::text,p.created_at FROM commerce_v1.provider_receipts p JOIN commerce_v1.subscriptions s ON s.id=p.subscription_id WHERE `+where+` ORDER BY p.created_at DESC,p.id OFFSET $2 LIMIT $3`, q.AccountID, q.Offset, q.Take)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v merchants.InboundOrder
		if err = rows.Scan(&v.ID, &v.Provider, &v.ExternalID, &v.SubscriptionID, &v.CreatedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.view"); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (r *Repository) AdminInboundOrder(ctx context.Context, actor, id string) (merchants.InboundOrder, error) {
	if !wallet.ValidID(id) {
		return merchants.InboundOrder{}, merchants.ErrInvalid
	}
	tx, err := r.begin(ctx, actor, "orders.view")
	if err != nil {
		return merchants.InboundOrder{}, err
	}
	defer tx.Rollback()
	var v merchants.InboundOrder
	err = tx.QueryRowContext(ctx, `SELECT p.id::text,p.provider,p.external_id,p.subscription_id::text,p.created_at FROM commerce_v1.provider_receipts p JOIN commerce_v1.subscriptions s ON s.id=p.subscription_id WHERE p.id=$1::uuid`, id).Scan(&v.ID, &v.Provider, &v.ExternalID, &v.SubscriptionID, &v.CreatedAt)
	if err != nil {
		return v, mapped(err)
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.view"); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (r *Repository) Billing(ctx context.Context, actor string, q merchants.AdminQuery) (merchants.AdminPage[merchants.InboundOrder], error) {
	q.AccountID = actor
	// Billing is deliberately the same safe receipt projection as the admin view;
	// it never returns provider tokens or stored subscription documents.
	out := merchants.AdminPage[merchants.InboundOrder]{Items: []merchants.InboundOrder{}}
	q, err := adminBounds(q)
	if err != nil {
		return out, err
	}
	tx, err := r.begin(ctx, actor, "")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM commerce_v1.provider_receipts p JOIN commerce_v1.subscriptions s ON s.id=p.subscription_id WHERE s.account_id=$1::uuid`, actor).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id::text,p.provider,p.external_id,p.subscription_id::text,p.created_at FROM commerce_v1.provider_receipts p JOIN commerce_v1.subscriptions s ON s.id=p.subscription_id WHERE s.account_id=$1::uuid ORDER BY p.created_at DESC,p.id OFFSET $2 LIMIT $3`, actor, q.Offset, q.Take)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v merchants.InboundOrder
		if err = rows.Scan(&v.ID, &v.Provider, &v.ExternalID, &v.SubscriptionID, &v.CreatedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if _, err = r.guard.Authenticate(ctx, r.db, tx, actor); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (r *Repository) AdjustBalance(ctx context.Context, actor string, req merchants.BalanceAdjustment, key string) (wallet.Transaction, error) {
	if key == "" || len(key) > 128 || !wallet.ValidCurrency(req.Currency) || req.Amount.Sign() == 0 || !wallet.ValidID(req.TreasuryWalletID) || ((req.WalletID == "") == (req.AccountID == "")) || req.WalletID != "" && !wallet.ValidID(req.WalletID) || req.AccountID != "" && !wallet.ValidID(req.AccountID) {
		return wallet.Transaction{}, merchants.ErrInvalid
	}
	tx, err := r.begin(ctx, actor, "wallets.balance.modify")
	if err != nil {
		return wallet.Transaction{}, err
	}
	defer tx.Rollback()
	target := req.WalletID
	if target == "" {
		err = tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, req.AccountID).Scan(&target)
	} else {
		var ok bool
		err = tx.QueryRowContext(ctx, `SELECT true FROM wallet_v1.wallets WHERE id=$1::uuid AND deleted_at IS NULL`, target).Scan(&ok)
	}
	if err != nil {
		return wallet.Transaction{}, mapped(err)
	}
	payer, payee, amount := req.TreasuryWalletID, target, req.Amount
	if amount.Sign() < 0 {
		payer, payee = target, req.TreasuryWalletID
		amount, _ = wallet.ParseAmount(strings.TrimPrefix(amount.String(), "-"))
	}
	out, err := walletpg.FundedTransfer(ctx, tx, actor, payer, payee, req.Currency, amount, req.Remark, scopedWalletKey("balance", key), r.now().UTC())
	if err != nil {
		return out, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "wallets.balance.modify"); err != nil {
		return wallet.Transaction{}, err
	}
	return out, tx.Commit()
}

func (r *Repository) AdminSubscriptions(ctx context.Context, actor string, q merchants.AdminQuery) (merchants.AdminPage[c.Subscription], error) {
	out := merchants.AdminPage[c.Subscription]{Items: []c.Subscription{}}
	q, err := adminBounds(q)
	if err != nil {
		return out, err
	}
	tx, err := r.begin(ctx, actor, "subscriptions.order.manage")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	where := `($1='' OR account_id::text=$1) AND($2='' OR identifier=$2) AND($3::integer IS NULL OR status=$3)`
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM commerce_v1.subscriptions WHERE `+where, q.AccountID, q.Identifier, q.Status).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT document FROM commerce_v1.subscriptions WHERE `+where+` ORDER BY begun_at DESC,id DESC OFFSET $4 LIMIT $5`, q.AccountID, q.Identifier, q.Status, q.Offset, q.Take)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		var v c.Subscription
		if err = json.Unmarshal(raw, &v); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "subscriptions.order.manage"); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (r *Repository) MaintenanceFailures(ctx context.Context, actor, after string, limit int) ([]merchants.MaintenanceFailure, error) {
	if r.maintenance == nil {
		return nil, merchants.ErrUnavailable
	}
	rows, err := r.maintenance.OperatorSubscriptionMaintenanceFailures(ctx, actor, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]merchants.MaintenanceFailure, 0, len(rows))
	for _, v := range rows {
		out = append(out, merchants.MaintenanceFailure{SubscriptionID: v.SubscriptionID, Reason: v.Reason, DocumentSHA256: v.DocumentSHA256, FirstSeenAt: v.FirstSeenAt, LastSeenAt: v.LastSeenAt, ResolvedAt: v.ResolvedAt, Observations: v.Observations})
	}
	return out, nil
}
func (r *Repository) RetryMaintenance(ctx context.Context, actor, id string) error {
	if r.maintenance == nil {
		return merchants.ErrUnavailable
	}
	return r.maintenance.OperatorRetrySubscriptionMaintenance(ctx, actor, id)
}
func (r *Repository) RunMaintenance(ctx context.Context, actor string, limit int) (int, error) {
	if r.maintenance == nil {
		return 0, merchants.ErrUnavailable
	}
	return r.maintenance.OperatorProcessDue(ctx, actor, limit)
}

func (r *Repository) WalletProductSummary(ctx context.Context, actor string) (json.RawMessage, error) {
	tx, err := r.begin(ctx, actor, "orders.view")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if len(r.walletProduct) == 0 {
		return nil, merchants.ErrUnavailable
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.view"); err != nil {
		return nil, err
	}
	out := append(json.RawMessage(nil), r.walletProduct...)
	return out, tx.Commit()
}

func (r *Repository) ApplyWalletProduct(ctx context.Context, actor, orderID string, req merchants.WalletProductApply, key string) (wallet.Transaction, error) {
	if key == "" || len(key) > 128 || !wallet.ValidID(orderID) || !wallet.ValidID(req.TreasuryWalletID) || len(r.walletProduct) == 0 {
		return wallet.Transaction{}, merchants.ErrInvalid
	}
	if !wallet.ValidID(r.walletProductTreasury) {
		return wallet.Transaction{}, merchants.ErrUnavailable
	}
	if req.TreasuryWalletID != r.walletProductTreasury {
		return wallet.Transaction{}, merchants.ErrForbidden
	}
	var product struct {
		Identifier *string `json:"identifier"`
		Currency   *string `json:"currency"`
	}
	if json.Unmarshal(r.walletProduct, &product) != nil || product.Identifier == nil || product.Currency == nil {
		return wallet.Transaction{}, merchants.ErrUnavailable
	}
	tx, err := r.begin(ctx, actor, "orders.pay")
	if err != nil {
		return wallet.Transaction{}, err
	}
	defer tx.Rollback()
	var raw []byte
	var account string
	err = tx.QueryRowContext(ctx, `SELECT o.document,o.actor_id::text FROM commerce_v1.orders o WHERE o.id=$1::uuid FOR UPDATE`, orderID).Scan(&raw, &account)
	if err != nil {
		return wallet.Transaction{}, mapped(err)
	}
	var o c.Order
	if err = json.Unmarshal(raw, &o); err != nil {
		return wallet.Transaction{}, err
	}
	if o.AppIdentifier != "internal" || o.ProductIdentifier == nil || *o.ProductIdentifier != *product.Identifier || o.Currency != *product.Currency || !wallet.ValidID(account) {
		return wallet.Transaction{}, merchants.ErrConflict
	}
	var target string
	if err = tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, account).Scan(&target); err != nil {
		return wallet.Transaction{}, mapped(err)
	}
	if o.PayeeWalletID != target {
		return wallet.Transaction{}, merchants.ErrConflict
	}
	transactionKey := scopedWalletKey("wallet-product:"+orderID, key)
	if o.Status == c.Finished && o.TransactionID != nil {
		out, e := scanAdminTransaction(tx.QueryRowContext(ctx, `SELECT `+adminTransactionCols+` FROM wallet_v1.transactions WHERE id=$1::uuid AND actor_id=$2::uuid AND idempotency_key=$3 AND payer_wallet_id=$4::uuid AND payee_wallet_id=$5::uuid`, *o.TransactionID, actor, transactionKey, req.TreasuryWalletID, target))
		if e != nil {
			return out, merchants.ErrConflict
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
			return wallet.Transaction{}, e
		}
		return out, tx.Commit()
	}
	if o.Status != c.Paid || o.TransactionID != nil {
		return wallet.Transaction{}, merchants.ErrConflict
	}
	if o.PayerWalletID != nil {
		return wallet.Transaction{}, merchants.ErrConflict
	}
	out, err := walletpg.FundedTransfer(ctx, tx, actor, r.walletProductTreasury, target, o.Currency, o.Amount, o.Remarks, transactionKey, r.now().UTC())
	if err != nil {
		return out, err
	}
	o.Status = c.Finished
	o.TransactionID = &out.ID
	o.PayerWalletID = &r.walletProductTreasury
	o.UpdatedAt = r.now().UTC()
	raw, err = json.Marshal(o)
	if err != nil {
		return wallet.Transaction{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE commerce_v1.orders SET status=$2,payer_wallet_id=$3::uuid,transaction_id=$4::uuid,document=$5::jsonb WHERE id=$1::uuid`, orderID, o.Status, r.walletProductTreasury, out.ID, raw); err != nil {
		return wallet.Transaction{}, err
	}
	if err = commerceEvent(ctx, tx, "order.finished", orderID, o, o.UpdatedAt); err != nil {
		return wallet.Transaction{}, err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); err != nil {
		return wallet.Transaction{}, err
	}
	return out, tx.Commit()
}
