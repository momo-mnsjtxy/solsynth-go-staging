// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	s "solsynth.local/go-migration/ports/sphere/postawards"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

type PostAwardFulfillment interface {
	PreparePostAward(context.Context, *sql.DB, *sql.Tx, string, c.Order, string, time.Time) error
	FulfillPostAward(context.Context, *sql.DB, *sql.Tx, string, c.Order, time.Time) error
}

func (r *Repository) WithPostAwards(f PostAwardFulfillment) *Repository { r.postAwards = f; return r }
func postAwardError(e error) error {
	switch {
	case errors.Is(e, s.ErrRetry):
		return c.ErrRetry
	case errors.Is(e, s.ErrConflict):
		return c.ErrConflict
	case errors.Is(e, s.ErrNotFound):
		return c.ErrNotFound
	case errors.Is(e, s.ErrForbidden):
		return c.ErrForbidden
	case errors.Is(e, s.ErrInvalid):
		return c.ErrInvalid
	case errors.Is(e, s.ErrWallet):
		return c.ErrConflict
	case errors.Is(e, s.ErrUnavailable):
		return c.ErrUnavailable
	}
	return e
}
func (r *Repository) postAwardBoundary(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) error {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil {
		return c.ErrUnavailable
	}
	_, e := r.guard.Authorize(ctx, db, tx, actor, "posts.award")
	return e
}
func (r *Repository) PostAwardRequest(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, key, fp, resource string) (string, error) {
	if e := r.postAwardBoundary(ctx, db, tx, actor); e != nil {
		return "", e
	}
	if len(key) > 128 || len(fp) != 64 {
		return "", c.ErrInvalid
	}
	if resource == "" {
		return idem(ctx, tx, "post-award:"+actor, key, "post-award", fp)
	}
	if !authority.ValidID(resource) {
		return "", c.ErrInvalid
	}
	return resource, recordIdem(ctx, tx, "post-award:"+actor, key, "post-award", fp, resource)
}
func (r *Repository) CreatePostAwardOrder(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purchase, post, payee string, amount wallet.Amount, attitude int, expiry, at time.Time) (out c.Order, e error) {
	if e = r.postAwardBoundary(ctx, db, tx, actor); e != nil {
		return
	}
	if !authority.ValidID(purchase) || !authority.ValidID(post) || !authority.ValidID(payee) || amount.Sign() <= 0 || (attitude != s.Positive && attitude != s.Negative) || !expiry.After(at) || expiry.Sub(at) > 24*time.Hour {
		return out, c.ErrInvalid
	}
	oid, e := id()
	if e != nil {
		return out, e
	}
	product, remark := s.Product, "Award post"
	meta := map[string]json.RawMessage{}
	for k, v := range map[string]any{"account_id": actor, "post_id": post, "purchase_id": purchase, "amount": amount.String(), "attitude": attitude} {
		b, x := json.Marshal(v)
		if x != nil {
			return out, x
		}
		meta[k] = b
	}
	out = c.Order{ID: oid, Status: c.Unpaid, Currency: s.Currency, Amount: amount, AppIdentifier: "internal", ProductIdentifier: &product, Remarks: &remark, Meta: meta, PayeeWalletID: payee, Items: []c.OrderItem{}, CreatedAt: at, UpdatedAt: at, ExpiredAt: expiry}
	return out, insertOrder(ctx, tx, out, "", actor, "", "")
}

// ReservePostAwardShare can reserve only the exact captured share of a verified
// paid source award. It uses the existing wallet hold, never a nil-payer credit.
func (r *Repository) ReservePostAwardShare(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purchase, treasuryActor, beneficiary string, original c.Order, share wallet.Amount, now time.Time) (out c.Order, e error) {
	if e = r.postAwardBoundary(ctx, db, tx, actor); e != nil {
		return
	}
	if original.AppIdentifier != "internal" || original.ProductIdentifier == nil || *original.ProductIdentifier != s.Product || original.TransactionID == nil || !authority.ValidID(purchase) || !authority.ValidID(treasuryActor) || !authority.ValidID(beneficiary) || share.Sign() <= 0 {
		return out, c.ErrInvalid
	}
	oid, e := id()
	if e != nil {
		return out, e
	}
	product, remark := s.DistributionProduct, "Reserved settled post award share"
	p, _ := json.Marshal(purchase)
	source, _ := json.Marshal(original.ID)
	payer := original.PayeeWalletID
	out = c.Order{ID: oid, Status: c.Paid, Currency: s.Currency, Amount: share, AppIdentifier: "internal", ProductIdentifier: &product, Remarks: &remark, Meta: map[string]json.RawMessage{"purchase_id": p, "source_order_id": source}, PayerWalletID: &payer, PayeeWalletID: beneficiary, Items: []c.OrderItem{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(24 * time.Hour)}
	if e = insertOrder(ctx, tx, out, "", treasuryActor, "", ""); e != nil {
		return out, e
	}
	tr, e := walletpg.ReservePostAwardShare(ctx, tx, original.ID, *original.TransactionID, out.ID, treasuryActor, payer, beneficiary, share, now)
	if e != nil {
		return out, e
	}
	out.TransactionID = &tr.ID
	return out, saveOrder(ctx, tx, out)
}

// ReleasePostAwardShare consumes only an existing matching paid receipt/reserve.
// Calling it cannot create an award, choose another beneficiary or mint funds.
func (r *Repository) ReleasePostAwardShare(ctx context.Context, db *sql.DB, tx *sql.Tx, purchase string, now time.Time) error {
	if r == nil || r.db != db || db == nil || tx == nil || !authority.ValidID(purchase) {
		return c.ErrUnavailable
	}
	var distribution string
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT r.distribution_order_id::text,EXISTS(SELECT 1 FROM commerce_v1.orders o JOIN wallet_v1.transactions paid ON paid.id=r.transaction_id JOIN commerce_v1.orders d ON d.id=r.distribution_order_id JOIN wallet_v1.transactions reserve ON reserve.id=r.reserve_transaction_id WHERE o.id=p.order_id AND o.actor_id=p.account_id AND o.status=3 AND o.transaction_id=paid.id AND o.app_id IS NULL AND o.product_identifier='posts.award' AND paid.managed_by=o.id AND paid.actor_id=p.account_id AND paid.purpose='commerce' AND paid.status=2 AND paid.deleted_at IS NULL AND paid.amount=p.amount AND paid.currency='points' AND paid.payee_wallet_id=p.treasury_wallet_id AND d.app_id IS NULL AND d.product_identifier='posts.award.distribution' AND d.status IN(1,3) AND d.transaction_id=reserve.id AND d.actor_id=p.treasury_account_id AND d.document->'meta'->>'source_order_id'=o.id::text AND d.document->'meta'->>'purchase_id'=p.id::text AND d.payer_wallet_id=p.treasury_wallet_id AND d.payee_wallet_id=p.beneficiary_wallet_id AND d.amount=p.share_amount AND d.currency='points' AND reserve.managed_by=d.id AND reserve.actor_id=p.treasury_account_id AND reserve.purpose='commerce' AND reserve.status IN(1,2) AND reserve.deleted_at IS NULL AND reserve.amount=p.share_amount AND reserve.currency='points' AND reserve.payer_wallet_id=p.treasury_wallet_id AND reserve.payee_wallet_id=p.beneficiary_wallet_id AND p.fulfilled_at IS NOT NULL AND p.attitude=0 AND r.due_at<=$2::timestamptz) FROM owned_post_award_receipts r JOIN owned_post_award_purchases p ON p.id=r.purchase_id WHERE p.id=$1::uuid`, purchase, now).Scan(&distribution, &valid)
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
	o.UpdatedAt = now
	if e = saveOrder(ctx, tx, o); e != nil {
		return e
	}
	return event(ctx, tx, "order.finished", o.ID, o, now)
}
