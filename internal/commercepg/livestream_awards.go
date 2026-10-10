// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

type LiveStreamAwardFulfillment interface {
	PrepareLiveStreamAward(context.Context, *sql.DB, *sql.Tx, string, c.Order, string, time.Time) error
	FulfillLiveStreamAward(context.Context, *sql.DB, *sql.Tx, string, c.Order, time.Time) error
}

func (r *Repository) WithLiveStreamAwards(f LiveStreamAwardFulfillment) *Repository {
	r.liveAwards = f
	return r
}
func awardError(e error) error {
	var pg *pgconn.PgError
	if errors.Is(e, live.ErrAwardRetry) || errors.As(e, &pg) && pg.Code == "55P03" {
		return c.ErrRetry
	}
	switch {
	case errors.Is(e, live.ErrForbidden):
		return c.ErrForbidden
	case errors.Is(e, live.ErrNotFound):
		return c.ErrNotFound
	case errors.Is(e, live.ErrInvalid):
		return c.ErrInvalid
	case errors.Is(e, live.ErrConflict):
		return c.ErrConflict
	case errors.Is(e, live.ErrAwardUnavailable), errors.Is(e, live.ErrUnconfigured), errors.Is(e, live.ErrUnavailable):
		return c.ErrUnavailable
	}
	return e
}
func (r *Repository) awardBoundary(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) error {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil || !authority.ValidID(actor) {
		return c.ErrUnavailable
	}
	_, e := r.guard.Authenticate(ctx, db, tx, actor)
	return e
}
func (r *Repository) LiveAwardRequest(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, key, fp, resource string) (string, error) {
	if e := r.awardBoundary(ctx, db, tx, actor); e != nil {
		return "", e
	}
	if len(key) > 128 || len(fp) != 64 {
		return "", c.ErrInvalid
	}
	if resource == "" {
		return idem(ctx, tx, "live-award:"+actor, key, "live.award", fp)
	}
	if !authority.ValidID(resource) {
		return "", c.ErrInvalid
	}
	return resource, recordIdem(ctx, tx, "live-award:"+actor, key, "live.award", fp, resource)
}
func (r *Repository) CreateLiveAwardOrder(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purchase, stream, currency, payee string, amount wallet.Amount, expiry, now time.Time) (c.Order, error) {
	var out c.Order
	if e := r.awardBoundary(ctx, db, tx, actor); e != nil {
		return out, e
	}
	if !authority.ValidID(purchase) || !authority.ValidID(stream) || !authority.ValidID(payee) || amount.Sign() <= 0 || currency == "" || len(currency) > 128 || !expiry.After(now) || expiry.Sub(now) > 24*time.Hour {
		return out, c.ErrInvalid
	}
	oid, e := id()
	if e != nil {
		return out, e
	}
	product, remark := live.AwardProduct, "Award livestream"
	a, _ := json.Marshal(actor)
	p, _ := json.Marshal(purchase)
	s, _ := json.Marshal(stream)
	now = now.UTC().Truncate(time.Microsecond)
	out = c.Order{ID: oid, Status: c.Unpaid, Currency: currency, Amount: amount, AppIdentifier: "internal", ProductIdentifier: &product, Remarks: &remark, Meta: map[string]json.RawMessage{"account_id": a, "purchase_id": p, "livestream_id": s}, PayeeWalletID: payee, Items: []c.OrderItem{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: expiry.UTC().Truncate(time.Microsecond)}
	return out, insertOrder(ctx, tx, out, "", actor, "", "")
}

// ReserveLiveAwardShare creates a fixed internal paid distribution order and a
// matching canonical wallet hold from the original, just-settled treasury credit.
func (r *Repository) ReserveLiveAwardShare(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purchase, treasuryActor, beneficiary string, original c.Order, share wallet.Amount, now time.Time) (c.Order, error) {
	var out c.Order
	if e := r.awardBoundary(ctx, db, tx, actor); e != nil {
		return out, e
	}
	if original.AppIdentifier != "internal" || original.ProductIdentifier == nil || *original.ProductIdentifier != live.AwardProduct || original.TransactionID == nil || !authority.ValidID(purchase) || !authority.ValidID(treasuryActor) || !authority.ValidID(beneficiary) {
		return out, c.ErrInvalid
	}
	oid, e := id()
	if e != nil {
		return out, e
	}
	product, remark := live.AwardDistributionProduct, "Reserved settled livestream award share"
	p, _ := json.Marshal(purchase)
	o, _ := json.Marshal(original.ID)
	payer := original.PayeeWalletID
	now = now.UTC().Truncate(time.Microsecond)
	out = c.Order{ID: oid, Status: c.Paid, Currency: original.Currency, Amount: share, AppIdentifier: "internal", ProductIdentifier: &product, Remarks: &remark, Meta: map[string]json.RawMessage{"purchase_id": p, "source_order_id": o}, PayerWalletID: &payer, PayeeWalletID: beneficiary, Items: []c.OrderItem{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(24 * time.Hour)}
	if e = insertOrder(ctx, tx, out, "", treasuryActor, "", ""); e != nil {
		return out, e
	}
	tr, e := walletpg.ReserveAwardShare(ctx, tx, original.ID, *original.TransactionID, out.ID, treasuryActor, payer, beneficiary, share, now)
	if e != nil {
		return out, e
	}
	out.TransactionID = &tr.ID
	if e = saveOrder(ctx, tx, out); e != nil {
		return out, e
	}
	return out, nil
}

// ReleaseLiveAwardShare accepts only an existing immutable award receipt after
// its source run ended. It cannot create a hold, payment, principal or new award.
func (r *Repository) ReleaseLiveAwardShare(ctx context.Context, db *sql.DB, tx *sql.Tx, purchase string, now time.Time) error {
	if r == nil || r.db != db || db == nil || tx == nil || !authority.ValidID(purchase) {
		return c.ErrUnavailable
	}
	var distribution string
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT r.distribution_order_id::text,EXISTS(SELECT 1 FROM commerce_v1.orders o JOIN wallet_v1.transactions paid ON paid.id=r.transaction_id JOIN commerce_v1.orders d ON d.id=r.distribution_order_id JOIN wallet_v1.transactions reserve ON reserve.id=r.reserve_transaction_id JOIN owned_livestream_runs run ON run.stream_id=p.stream_id AND run.generation=p.generation WHERE o.id=p.order_id AND o.id=r.order_id AND o.actor_id=p.account_id AND o.status=3 AND o.transaction_id=paid.id AND o.app_id IS NULL AND o.product_identifier='livestreams.award' AND paid.managed_by=o.id AND paid.actor_id=p.account_id AND paid.purpose='commerce' AND paid.status=2 AND paid.deleted_at IS NULL AND paid.amount=p.amount AND paid.currency=p.currency AND paid.payee_wallet_id=p.treasury_wallet_id AND d.app_id IS NULL AND d.product_identifier='livestreams.award.distribution' AND d.status IN(1,3) AND d.transaction_id=reserve.id AND d.actor_id=p.treasury_account_id AND d.document->'meta'->>'source_order_id'=o.id::text AND d.payer_wallet_id=p.treasury_wallet_id AND d.payee_wallet_id=p.beneficiary_wallet_id AND d.amount=p.share_amount AND d.currency=p.currency AND reserve.managed_by=d.id AND reserve.actor_id=p.treasury_account_id AND reserve.purpose='commerce' AND reserve.status IN(1,2) AND reserve.deleted_at IS NULL AND reserve.amount=p.share_amount AND reserve.currency=p.currency AND reserve.payer_wallet_id=p.treasury_wallet_id AND reserve.payee_wallet_id=p.beneficiary_wallet_id AND run.phase IN('ending','done')) FROM owned_livestream_award_receipts r JOIN owned_livestream_award_purchases p ON p.id=r.purchase_id WHERE r.purchase_id=$1::uuid`, purchase).Scan(&distribution, &valid)
	if e != nil {
		return e
	}
	if !valid {
		return c.ErrConflict
	}
	o, e := order(ctx, tx, distribution, true)
	if e != nil {
		return e
	}
	if o.Status == c.Finished {
		return nil
	}
	if o.Status != c.Paid {
		return c.ErrConflict
	}
	if _, e = walletpg.CommerceSettle(ctx, tx, distribution, false, now); e != nil {
		return e
	}
	o.Status = c.Finished
	o.UpdatedAt = now.UTC().Truncate(time.Microsecond)
	if e = saveOrder(ctx, tx, o); e != nil {
		return e
	}
	return event(ctx, tx, "order.finished", o.ID, o, now)
}

// ReleaseLiveAwardRefundReserve is the first durable refund phase. It returns
// the still-frozen 90% distribution to the captured treasury. The receipt is
// advanced by the caller only in the same transaction.
func (r *Repository) ReleaseLiveAwardRefundReserve(ctx context.Context, db *sql.DB, tx *sql.Tx, purchase string, now time.Time) error {
	if r == nil || r.db != db || db == nil || tx == nil || !authority.ValidID(purchase) {
		return c.ErrUnavailable
	}
	var distribution string
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT r.distribution_order_id::text,EXISTS(SELECT 1 FROM commerce_v1.orders d JOIN wallet_v1.transactions reserve ON reserve.id=r.reserve_transaction_id WHERE d.id=r.distribution_order_id AND d.app_id IS NULL AND d.product_identifier='livestreams.award.distribution' AND d.status IN(1,2) AND d.transaction_id=reserve.id AND d.actor_id=p.treasury_account_id AND d.payer_wallet_id=p.treasury_wallet_id AND d.payee_wallet_id=p.beneficiary_wallet_id AND d.amount=p.share_amount AND d.currency=p.currency AND reserve.managed_by=d.id AND reserve.actor_id=p.treasury_account_id AND reserve.purpose='commerce' AND reserve.status IN(1,3) AND reserve.deleted_at IS NULL AND reserve.amount=p.share_amount AND reserve.currency=p.currency AND reserve.payer_wallet_id=p.treasury_wallet_id AND reserve.payee_wallet_id=p.beneficiary_wallet_id) FROM owned_livestream_award_receipts r JOIN owned_livestream_award_purchases p ON p.id=r.purchase_id WHERE r.purchase_id=$1::uuid AND r.payout_state='refund_pending'`, purchase).Scan(&distribution, &valid)
	if e != nil {
		return e
	}
	if !valid {
		return c.ErrConflict
	}
	o, e := order(ctx, tx, distribution, true)
	if e != nil {
		return e
	}
	if o.Status == c.Cancelled {
		return nil
	}
	if o.Status != c.Paid {
		return c.ErrConflict
	}
	if _, e = walletpg.CommerceSettle(ctx, tx, distribution, true, now); e != nil {
		return e
	}
	o.Status, o.UpdatedAt = c.Cancelled, now.UTC().Truncate(time.Microsecond)
	if e = saveOrder(ctx, tx, o); e != nil {
		return e
	}
	return event(ctx, tx, "order.cancelled", o.ID, o, now)
}

// ReverseLiveAwardPayment is the second refund phase. The reserve must already
// be returned, so the fixed treasury has the full captured amount before the
// canonical FundedReversal executes. Its idempotency key is purchase-bound.
func (r *Repository) ReverseLiveAwardPayment(ctx context.Context, db *sql.DB, tx *sql.Tx, purchase string, now time.Time) (string, error) {
	if r == nil || r.db != db || db == nil || tx == nil || !authority.ValidID(purchase) {
		return "", c.ErrUnavailable
	}
	var original, treasuryActor, treasury, payer, currency string
	var amount wallet.Amount
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT o.id::text,p.treasury_account_id::text,p.treasury_wallet_id::text,paid.payer_wallet_id::text,p.currency,p.amount::text,EXISTS(SELECT 1 FROM commerce_v1.orders d JOIN wallet_v1.transactions reserve ON reserve.id=r.reserve_transaction_id JOIN wallet_v1.wallets tw ON tw.id=p.treasury_wallet_id WHERE d.id=r.distribution_order_id AND d.status=2 AND reserve.managed_by=d.id AND reserve.status=3 AND reserve.deleted_at IS NULL AND tw.account_id=p.treasury_account_id AND tw.realm_id IS NULL AND tw.deleted_at IS NULL) FROM owned_livestream_award_receipts r JOIN owned_livestream_award_purchases p ON p.id=r.purchase_id JOIN commerce_v1.orders o ON o.id=p.order_id AND o.id=r.order_id JOIN wallet_v1.transactions paid ON paid.id=r.transaction_id AND paid.id=o.transaction_id WHERE r.purchase_id=$1::uuid AND r.payout_state='refund_ready' AND o.app_id IS NULL AND o.product_identifier='livestreams.award' AND o.status=3 AND paid.managed_by=o.id AND paid.actor_id=p.account_id AND paid.purpose='commerce' AND paid.status=2 AND paid.confirmed_at IS NOT NULL AND paid.deleted_at IS NULL AND paid.amount=p.amount AND paid.currency=p.currency AND paid.payee_wallet_id=p.treasury_wallet_id`, purchase).Scan(&original, &treasuryActor, &treasury, &payer, &currency, &amount, &valid)
	if e != nil {
		return "", e
	}
	if !valid {
		return "", c.ErrConflict
	}
	o, e := order(ctx, tx, original, true)
	if e != nil {
		return "", e
	}
	remark := "Refund livestream award " + purchase
	reversal, e := walletpg.FundedReversal(ctx, tx, treasuryActor, treasury, payer, currency, amount, &remark, "livestream-award-refund:"+purchase, now)
	if e != nil {
		return "", e
	}
	var stillBound bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE id=$1::uuid AND account_id=$2::uuid AND realm_id IS NULL AND deleted_at IS NULL)`, treasury, treasuryActor).Scan(&stillBound); e != nil {
		return "", e
	}
	if !stillBound {
		return "", c.ErrConflict
	}
	o.Status, o.UpdatedAt = c.Cancelled, now.UTC().Truncate(time.Microsecond)
	if e = saveOrder(ctx, tx, o); e != nil {
		return "", e
	}
	if e = event(ctx, tx, "order.refunded", o.ID, o, now); e != nil {
		return "", e
	}
	return reversal.ID, nil
}
