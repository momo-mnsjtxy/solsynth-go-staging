// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

func giftRow(ctx context.Context, tx *sql.Tx, idv string, locked bool) (c.Gift, error) {
	suffix := ""
	if locked {
		suffix = " FOR UPDATE"
	}
	return decode[c.Gift](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.gifts WHERE id=$1`+suffix, idv))
}
func saveGift(ctx context.Context, tx *sql.Tx, g c.Gift) error {
	b, e := jsonBytes(g)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE commerce_v1.gifts SET status=$2,redeemer_id=$3,subscription_id=$4,document=$5 WHERE id=$1`, g.ID, g.Status, g.RedeemerID, g.SubscriptionID, b)
	return e
}
func (r *Repository) PurchaseGift(ctx context.Context, actor string, req c.PurchaseGift, key string) (c.Gift, error) {
	var g c.Gift
	tx, e := r.userTx(ctx, actor, "subscriptions.gifts.purchase")
	if e != nil {
		return g, e
	}
	defer tx.Rollback()
	if req.PaymentMethod != "solian.wallet" {
		return g, c.ErrUnavailable
	}
	d, e := definition(ctx, tx, req.SubscriptionIdentifier)
	if e != nil {
		return g, e
	}
	allowed := false
	for _, m := range d.AllowedPaymentMethods {
		if m == req.PaymentMethod {
			allowed = true
		}
	}
	if !allowed {
		return g, c.ErrForbidden
	}

	if !d.AllowGift {
		return g, c.ErrForbidden
	}
	days := 30
	if req.GiftDurationDays != nil {
		days = *req.GiftDurationDays
	}
	cycle := d.CycleDurationDays
	if req.SubscriptionDurationDays != nil && *req.SubscriptionDurationDays != cycle {
		return g, c.ErrInvalid
	}
	if days < 1 || days > 366 || req.Message != nil && len(*req.Message) > 4096 {
		return g, c.ErrInvalid
	}
	if req.RecipientID != nil {
		if !wallet.ValidID(*req.RecipientID) || *req.RecipientID == actor {
			return g, c.ErrInvalid
		}
		var exists bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_accounts WHERE id=$1 AND NOT disabled)`, *req.RecipientID).Scan(&exists)
		if e != nil {
			return g, e
		}
		if !exists {
			return g, c.ErrNotFound
		}
	}
	fp, e := fingerprint(req)
	if e != nil {
		return g, e
	}
	prior, e := idem(ctx, tx, "account:"+actor, key, "gift", fp)
	if e != nil {
		return g, e
	}
	if prior != "" {
		g, e = giftRow(ctx, tx, prior, false)
		if e != nil {
			return g, e
		}
		return g, tx.Commit()
	}
	payer, e := defaultWallet(ctx, tx, actor, req.PaymentDetails.PayerWalletID)
	if e != nil {
		return g, e
	}
	if req.PaymentDetails.Currency != "" && req.PaymentDetails.Currency != d.Currency {
		return g, c.ErrInvalid
	}
	gid, e := id()
	if e != nil {
		return g, e
	}
	random := make([]byte, 16)
	if _, e = rand.Read(random); e != nil {
		return g, e
	}
	now := r.now().UTC()
	g = c.Gift{ID: gid, GifterID: actor, RecipientID: req.RecipientID, GiftCode: hex.EncodeToString(random), SubscriptionIdentifier: d.Identifier, Message: req.Message, Status: c.GiftCreated, IsOpenGift: req.RecipientID == nil, BasePrice: d.BasePrice, FinalPrice: d.BasePrice, PaymentMethod: req.PaymentMethod, PaymentDetails: c.PaymentDetails{Currency: d.Currency, PayerWalletID: &payer}, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	if req.Coupon != nil {
		app, err := couponApp(d)
		if err != nil {
			return g, err
		}
		g.Coupon, e = r.quoteCoupon(ctx, tx, actor, app, d.PayeeWalletID, "gift", d.Currency, *req.Coupon, []string{d.Identifier}, d.BasePrice)
		if e != nil {
			return g, e
		}
		g.FinalPrice = g.Coupon.Net
	}
	b, e := jsonBytes(g)
	if e != nil {
		return g, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.gifts(id,gifter_id,recipient_id,code,status,expires_at,cycle_days,document) VALUES($1,$2,$3,$4,0,$5,$6,$7)`, g.ID, actor, g.RecipientID, g.GiftCode, g.ExpiresAt, cycle, b)
	if e != nil {
		return g, e
	}
	if e = recordIdem(ctx, tx, "account:"+actor, key, "gift", fp, g.ID); e != nil {
		return g, e
	}
	if e = event(ctx, tx, "gift.created", g.ID, g, now); e != nil {
		return g, e
	}
	if g.Coupon != nil {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "subscriptions.gifts.purchase"); e != nil {
			return g, e
		}
		if e = couponIssuer(ctx, tx, g.Coupon.AppID, g.Coupon.PublisherID, g.Coupon.PayeeWalletID); e != nil {
			return g, e
		}
		if !g.Coupon.ExpiresAt.After(r.now().UTC()) {
			return g, c.ErrConflict
		}
	}
	return g, tx.Commit()
}
func giftAccess(g c.Gift, actor string) bool {
	return g.GifterID == actor || g.RecipientID != nil && *g.RecipientID == actor || g.RedeemerID != nil && *g.RedeemerID == actor
}
func (r *Repository) Gift(ctx context.Context, actor, idv string) (c.Gift, error) {
	var g c.Gift
	if !wallet.ValidID(idv) {
		return g, c.ErrNotFound
	}
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return g, e
	}
	defer tx.Rollback()
	g, e = giftRow(ctx, tx, idv, false)
	if e != nil {
		return g, e
	}
	if !giftAccess(g, actor) {
		return g, c.ErrNotFound
	}
	return g, tx.Commit()
}
func (r *Repository) Gifts(ctx context.Context, actor, side string, q c.PageQuery) ([]c.Gift, int, error) {
	out := []c.Gift{}
	q = bounds(q)
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	where := `gifter_id=$1 AND status<>0`
	if side == "received" {
		where = `(recipient_id=$1 OR redeemer_id=$1)`
	} else if side != "sent" {
		return nil, 0, c.ErrInvalid
	}
	var total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM commerce_v1.gifts WHERE `+where, actor).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT document FROM commerce_v1.gifts WHERE `+where+` ORDER BY document->>'created_at' DESC,id DESC OFFSET $2 LIMIT $3`, actor, q.Offset, q.Take)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		var b []byte
		var g c.Gift
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &g)
		}
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, g)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}
func (r *Repository) GiftOrder(ctx context.Context, actor, gid string) (c.Order, error) {
	var o c.Order
	if !wallet.ValidID(gid) {
		return o, c.ErrNotFound
	}
	tx, e := r.userTx(ctx, actor, "subscriptions.order.manage")
	if e != nil {
		return o, e
	}
	defer tx.Rollback()
	g, e := giftRow(ctx, tx, gid, true)
	if e != nil {
		return o, e
	}
	if g.GifterID != actor {
		return o, c.ErrNotFound
	}
	if g.Status != c.GiftCreated || !g.ExpiresAt.After(r.now()) {
		return o, c.ErrConflict
	}
	d, e := definition(ctx, tx, g.SubscriptionIdentifier)
	if e != nil {
		return o, e
	}
	o, e = r.resourceOrder(ctx, tx, actor, d, "", g.ID, g.PaymentDetails.PayerWalletID, g.FinalPrice, g.Coupon)
	if e != nil {
		return o, e
	}
	return o, tx.Commit()
}
func (r *Repository) GiftAction(ctx context.Context, actor, gid string, send bool) (c.Gift, error) {
	var g c.Gift
	if !wallet.ValidID(gid) {
		return g, c.ErrNotFound
	}
	permission := "subscriptions.gifts.cancel"
	if send {
		permission = "subscriptions.gifts.send"
	}
	tx, e := r.userTx(ctx, actor, permission)
	if e != nil {
		return g, e
	}
	defer tx.Rollback()
	g, e = giftRow(ctx, tx, gid, true)
	if e != nil {
		return g, e
	}
	if g.GifterID != actor {
		return g, c.ErrNotFound
	}
	now := r.now().UTC()
	if send {
		if g.Status == c.GiftSent && g.ExpiresAt.After(now) {
			return g, tx.Commit()
		}
		return g, c.ErrConflict
	}
	if g.Status == c.GiftCancelled {
		return g, tx.Commit()
	}
	if g.Status != c.GiftCreated && g.Status != c.GiftSent {
		return g, c.ErrConflict
	}
	if e = r.cancelGift(ctx, tx, &g, c.GiftCancelled, now); e != nil {
		return g, e
	}
	return g, tx.Commit()
}
func (r *Repository) cancelGift(ctx context.Context, tx *sql.Tx, g *c.Gift, status c.GiftStatus, now time.Time) error {
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM commerce_v1.orders WHERE gift_id=$1 AND status IN(0,1) ORDER BY id FOR UPDATE`, g.ID)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var oid string
		if e = rows.Scan(&oid); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, oid)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, oid := range ids {
		o, e := order(ctx, tx, oid, false)
		if e != nil {
			return e
		}
		if o.Status == c.Paid {
			if _, e = walletpg.CommerceSettle(ctx, tx, oid, true, now); e != nil {
				return e
			}
		}
		o.Status = c.Cancelled
		o.UpdatedAt = now
		if e = saveOrder(ctx, tx, o); e != nil {
			return e
		}
		if e = event(ctx, tx, "order.cancelled", oid, o, now); e != nil {
			return e
		}
	}
	g.Status = status
	g.UpdatedAt = now
	if e = saveGift(ctx, tx, *g); e != nil {
		return e
	}
	kind := "gift.cancelled"
	if status == c.GiftExpired {
		kind = "gift.expired"
	}
	return event(ctx, tx, kind, g.ID, *g, now)
}
func (r *Repository) CheckGift(ctx context.Context, actor, code string) (c.GiftCheck, error) {
	var out c.GiftCheck
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if len(code) != 32 {
		return out, c.ErrNotFound
	}
	g, e := decode[c.Gift](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.gifts WHERE code=$1`, code))
	if e != nil {
		return out, e
	}
	out = c.GiftCheck{GiftCode: code, SubscriptionIdentifier: g.SubscriptionIdentifier, Message: g.Message}
	out.CanRedeem = g.Status == c.GiftSent && g.ExpiresAt.After(r.now()) && g.GifterID != actor && (g.RecipientID == nil || *g.RecipientID == actor)
	if !out.CanRedeem {
		out.Error = "Gift is not redeemable."
		if !giftAccess(g, actor) {
			out.Message = nil
		}
	}
	return out, tx.Commit()
}
func (r *Repository) RedeemGift(ctx context.Context, actor, code string) (c.Redemption, error) {
	var out c.Redemption
	tx, e := r.userTx(ctx, actor, "subscriptions.gifts.redeem")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if len(code) != 32 {
		return out, c.ErrNotFound
	}
	g, e := decode[c.Gift](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.gifts WHERE code=$1 FOR UPDATE`, code))
	if e != nil {
		return out, e
	}
	if g.Status == c.GiftRedeemed && g.RedeemerID != nil && *g.RedeemerID == actor && g.SubscriptionID != nil {
		s, e := subscriptionRow(ctx, tx, *g.SubscriptionID, false)
		if e != nil {
			return out, e
		}
		return c.Redemption{Gift: g, Subscription: s}, tx.Commit()
	}
	now := r.now().UTC()
	if g.Status != c.GiftSent || !g.ExpiresAt.After(now) || g.GifterID == actor || g.RecipientID != nil && *g.RecipientID != actor {
		return out, c.ErrConflict
	}
	var oid string
	var days int
	e = tx.QueryRowContext(ctx, `SELECT paid_order_id::text,cycle_days FROM commerce_v1.gifts WHERE id=$1`, g.ID).Scan(&oid, &days)
	if e != nil {
		return out, e
	}
	o, e := order(ctx, tx, oid, true)
	if e != nil {
		return out, e
	}
	if o.Status != c.Paid || o.Amount.Cmp(g.FinalPrice) != 0 || !sameCoupon(o.Coupon, g.Coupon) {
		return out, c.ErrConflict
	}
	d, e := definition(ctx, tx, g.SubscriptionIdentifier)
	if e != nil {
		return out, e
	}
	sid, e := id()
	if e != nil {
		return out, e
	}
	begin, e := groupStart(ctx, tx, actor, d.GroupIdentifier, d.Identifier, "", now, false)
	if e != nil {
		return out, e
	}

	s := c.Subscription{Coupon: g.Coupon, ID: sid, AccountID: actor, Identifier: d.Identifier, GroupIdentifier: d.GroupIdentifier, DisplayName: d.DisplayName, Status: c.SubscriptionActive, IsActive: true, PaymentMethod: "gift", PaymentDetails: c.PaymentDetails{Currency: g.PaymentDetails.Currency, OrderID: g.ID}, BasePrice: g.BasePrice, FinalPrice: g.FinalPrice, BegunAt: begin, EndedAt: begin.Add(time.Duration(days) * 24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	if g.Coupon != nil {
		s.AppIdentifier = d.AppIdentifier
	}
	if e = insertSubscription(ctx, tx, s, days, false); e != nil {
		return out, e
	}
	if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
		return out, e
	}
	o.Status = c.Finished
	o.UpdatedAt = now
	if e = saveOrder(ctx, tx, o); e != nil {
		return out, e
	}
	g.Status = c.GiftRedeemed
	g.RedeemerID = &actor
	g.SubscriptionID = &s.ID
	g.RedeemedAt = &now
	g.UpdatedAt = now
	if e = saveGift(ctx, tx, g); e != nil {
		return out, e
	}
	if e = event(ctx, tx, "subscription.activated", s.ID, s, now); e != nil {
		return out, e
	}
	if e = event(ctx, tx, "gift.redeemed", g.ID, g, now); e != nil {
		return out, e
	}
	if e = event(ctx, tx, "order.finished", o.ID, o, now); e != nil {
		return out, e
	}
	return c.Redemption{Gift: g, Subscription: s}, tx.Commit()
}
