// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/passport/namecards"
	realmboost "solsynth.local/go-migration/ports/passport/realmboosts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	postaward "solsynth.local/go-migration/ports/sphere/postawards"
	sponsor "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"time"
)

func (r *Repository) userTx(ctx context.Context, actor, permission string) (*sql.Tx, error) {
	if !wallet.ValidID(actor) {
		return nil, c.ErrForbidden
	}
	return r.begin(ctx, actor, permission)
}
func defaultWallet(ctx context.Context, tx *sql.Tx, actor string, requested *string) (string, error) {
	var wid string
	if requested != nil {
		if !wallet.ValidID(*requested) {
			return "", c.ErrInvalid
		}
		e := tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, *requested, actor).Scan(&wid)
		return wid, notFound(e)
	}
	e := tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1 AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, actor).Scan(&wid)
	return wid, notFound(e)
}
func (r *Repository) PayOrder(ctx context.Context, actor, idv string, requested *string) (c.Order, error) {
	var o c.Order
	if !wallet.ValidID(idv) {
		return o, c.ErrNotFound
	}
	tx, e := r.userTx(ctx, actor, "orders.pay")
	if e != nil {
		return o, e
	}
	defer tx.Rollback()
	if r.payments != nil {
		if e = r.payments.LockPayment(ctx, r.db, tx, actor, wallet.PaymentOrder, wallet.OrderPaymentBinding(idv, requested)); e != nil {
			return o, e
		}
	}
	// Canonical app ancestry precedes the order row, matching merchant status
	// writers and app management; a disabled app cannot take a new payment.
	peek, e := order(ctx, tx, idv, false)
	if e != nil {
		return o, e
	}
	if peek.Status == c.Unpaid {
		if e = lockOrderDefinition(ctx, tx, idv); e != nil {
			return o, e
		}
		if e = lockOrderApp(ctx, tx, peek); e != nil {
			return o, e
		}
		if peek.Coupon != nil && peek.Coupon.Scope == "order" {
			if e = lockCouponOrderProducts(ctx, tx, peek); e != nil {
				return o, e
			}
		}
		if _, _, e = prepareRecurringOrder(ctx, tx, actor, peek); e != nil {
			return o, e
		}
	}
	o, e = order(ctx, tx, idv, true)
	if e != nil {
		return o, e
	}
	payer, e := defaultWallet(ctx, tx, actor, requested)
	if e != nil {
		return o, e
	}
	if o.PayerWalletID != nil && *o.PayerWalletID != payer {
		return o, c.ErrForbidden
	}
	if o.Status == c.Paid || o.Status == c.Finished {
		if o.PayerWalletID == nil {
			return o, c.ErrConflict
		}
		if r.payments != nil {
			if e = r.payments.CheckPayment(ctx, r.db, tx, actor, wallet.PaymentOrder, wallet.OrderPaymentBinding(idv, requested)); e != nil {
				return o, e
			}
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
			return c.Order{}, e
		}
		return o, tx.Commit()
	}
	if o.Status != c.Unpaid {
		return o, c.ErrConflict
	}
	now := r.now().UTC()
	if !o.ExpiredAt.After(now) {
		o.Status = c.Expired
		o.UpdatedAt = now
		if e = saveOrder(ctx, tx, o); e != nil {
			return o, e
		}
		if e = event(ctx, tx, "order.expired", o.ID, o, now); e != nil {
			return o, e
		}
		if r.payments != nil {
			if e = r.payments.CheckPayment(ctx, r.db, tx, actor, wallet.PaymentOrder, wallet.OrderPaymentBinding(idv, requested)); e != nil {
				return o, e
			}
		}
		if e = tx.Commit(); e != nil {
			return o, e
		}
		return o, c.ErrConflict
	}
	var reserved, sub, gift sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT actor_id::text,subscription_id::text,gift_id::text FROM commerce_v1.orders WHERE id=$1`, idv).Scan(&reserved, &sub, &gift)
	if e != nil {
		return o, e
	}
	if reserved.Valid && reserved.String != actor {
		return o, c.ErrForbidden
	}
	// Authorization is rechecked after order/wallet waits and retained to commit.
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
		return o, e
	}
	isRealmBoost := o.AppIdentifier == "internal" && o.ProductIdentifier != nil && *o.ProductIdentifier == realmboost.Product
	if isRealmBoost {
		if r.realmBoosts == nil || sub.Valid || gift.Valid {
			return o, c.ErrUnavailable
		}
		if e = r.realmBoosts.PrepareRealmBoost(ctx, r.db, tx, actor, o, payer, now); e != nil {
			return o, realmBoostError(e)
		}
	}
	isLiveAward := o.AppIdentifier == "internal" && o.ProductIdentifier != nil && *o.ProductIdentifier == live.AwardProduct
	if isLiveAward {
		if r.liveAwards == nil || sub.Valid || gift.Valid {
			return o, c.ErrUnavailable
		}
		if e = r.liveAwards.PrepareLiveStreamAward(ctx, r.db, tx, actor, o, payer, now); e != nil {
			return o, awardError(e)
		}
	}
	isSponsor := o.AppIdentifier == "internal" && o.ProductIdentifier != nil && *o.ProductIdentifier == sponsor.Product
	if isSponsor {
		if r.sponsorship == nil || sub.Valid || gift.Valid {
			return o, c.ErrUnavailable
		}
		if e = r.sponsorship.PrepareSponsor(ctx, r.db, tx, actor, o, payer, now); e != nil {
			return o, sponsorError(e)
		}
	}
	isPostAward := o.AppIdentifier == "internal" && o.ProductIdentifier != nil && *o.ProductIdentifier == postaward.Product
	if isPostAward {
		if r.postAwards == nil || sub.Valid || gift.Valid {
			return o, c.ErrUnavailable
		}
		if e = r.postAwards.PreparePostAward(ctx, r.db, tx, actor, o, payer, now); e != nil {
			return o, postAwardError(e)
		}
	}

	recurring, isRecurring, e := prepareRecurringOrder(ctx, tx, actor, o)
	if e != nil {
		return o, e
	}
	if isRecurring && (sub.Valid || gift.Valid) {
		return o, c.ErrConflict
	}
	if e = r.captureDestination(ctx, tx, actor, &o); e != nil {
		return c.Order{}, e
	}
	if e = r.validateCouponQuote(ctx, tx, actor, o, false); e != nil {
		return o, e
	}
	tr, e := walletpg.CommerceHold(ctx, tx, o.ID, actor, payer, o.PayeeWalletID, o.Currency, o.Amount, now)
	if e != nil {
		return o, e
	}
	if e = redeemCoupon(ctx, tx, o, r.now().UTC()); e != nil {
		return o, e
	}
	o.Status = c.Paid
	o.PayerWalletID = &payer
	o.TransactionID = &tr.ID
	o.UpdatedAt = now
	if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.orders SET actor_id=$2 WHERE id=$1`, o.ID, actor); e != nil {
		return o, e
	}
	if isRecurring {
		if e = r.fulfillRecurringOrder(ctx, tx, actor, &o, recurring, now); e != nil {
			return o, e
		}
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
			return o, e
		}
		o.Status = c.Finished
	}
	if sub.Valid {
		if e = r.activateSubscription(ctx, tx, sub.String, o, now); e != nil {
			return o, e
		}
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
			return o, e
		}
		o.Status = c.Finished
	}
	if gift.Valid {
		g, e := giftRow(ctx, tx, gift.String, true)
		if e != nil {
			return o, e
		}
		if g.GifterID != actor || g.Status != c.GiftCreated || !g.ExpiresAt.After(now) {
			return o, c.ErrConflict
		}
		g.Status = c.GiftSent
		g.UpdatedAt = now
		if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.gifts SET paid_order_id=$2 WHERE id=$1`, g.ID, o.ID); e != nil {
			return o, e
		}
		if e = saveGift(ctx, tx, g); e != nil {
			return o, e
		}
		if e = event(ctx, tx, "gift.paid", g.ID, g, now); e != nil {
			return o, e
		}
	}
	if o.AppIdentifier == "internal" && o.ProductIdentifier != nil && *o.ProductIdentifier == namecards.Product {
		if r.namecards == nil || sub.Valid || gift.Valid {
			return o, c.ErrUnavailable
		}
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
			return o, e
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
			return o, e
		}
		if e = r.namecards.FulfillNameCard(ctx, r.db, tx, actor, o, now); e != nil {
			return o, e
		}
		o.Status = c.Finished
	}
	if isRealmBoost {
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
			return o, e
		}
		if e = r.realmBoosts.FulfillRealmBoost(ctx, r.db, tx, actor, o, now); e != nil {
			return o, realmBoostError(e)
		}
		o.Status = c.Finished
	}
	if isLiveAward {
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
			return o, e
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
			return o, e
		}
		if e = r.liveAwards.FulfillLiveStreamAward(ctx, r.db, tx, actor, o, now); e != nil {
			return o, awardError(e)
		}
		o.Status = c.Finished
	}
	if isPostAward {
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
			return o, e
		}
		if e = r.postAwards.FulfillPostAward(ctx, r.db, tx, actor, o, now); e != nil {
			return o, postAwardError(e)
		}
		o.Status = c.Finished
	}

	if isSponsor {
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
			return o, e
		}
		if e = r.sponsorship.FulfillSponsor(ctx, r.db, tx, actor, o, now); e != nil {
			return o, sponsorError(e)
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay", "posts.sponsor"); e != nil {
			return o, e
		}
		o.Status = c.Finished
	}
	if e = saveOrder(ctx, tx, o); e != nil {
		return o, e
	}
	if e = event(ctx, tx, "order.paid", o.ID, o, now); e != nil {
		return o, e
	}
	if isSponsor {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay", "posts.sponsor"); e != nil {
			return o, e
		}
	}
	if isPostAward {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "posts.award", "orders.pay"); e != nil {
			return o, e
		}
	}

	if isRecurring {
		if _, e = recurringConsent(ctx, tx, actor, recurring, 0); e != nil {
			return o, e
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
		return o, e
	}
	if r.payments != nil {
		if e = r.payments.CheckPayment(ctx, r.db, tx, actor, wallet.PaymentOrder, wallet.OrderPaymentBinding(idv, requested)); e != nil {
			return o, e
		}
	}
	if e = r.validateCouponQuote(ctx, tx, actor, o, true); e != nil {
		return c.Order{}, e
	}
	if isRealmBoost {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay", "realms.boosts.manage"); e != nil {
			return o, e
		}
	}
	// Wallet/fulfillment locks may have waited after the initial deadline check.
	// Rejecting here rolls back the hold, settlement, entitlement and outboxes
	// together; an earlier clock sample must not extend this checkout's lifetime.
	if !o.ExpiredAt.After(r.now().UTC()) {
		return c.Order{}, c.ErrConflict
	}
	if e = checkOrderAppOwner(ctx, tx, o); e != nil {
		return c.Order{}, e
	}
	return o, tx.Commit()
}
func (r *Repository) UpdateOrder(ctx context.Context, idv string, req c.UpdateStatus) (c.Order, error) {
	var o c.Order
	if !wallet.ValidID(idv) || (req.Status != c.Finished && req.Status != c.Cancelled) {
		return o, c.ErrInvalid
	}
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return o, e
	}
	defer tx.Rollback()
	app, e := r.client(ctx, tx, req.Credentials, "orders.status.manage")
	if e != nil {
		return o, e
	}
	o, e = order(ctx, tx, idv, true)
	if e != nil {
		return o, e
	}
	if o.AppIdentifier != "developer.app:"+app.AppID {
		return o, c.ErrNotFound
	}
	// App coupon issuance never delegates gift escrow settlement.
	var giftManaged bool
	if e = tx.QueryRowContext(ctx, `SELECT gift_id IS NOT NULL FROM commerce_v1.orders WHERE id=$1`, o.ID).Scan(&giftManaged); e != nil {
		return o, e
	}
	if giftManaged {
		return o, c.ErrForbidden
	}
	if o.Status == req.Status {
		if _, e = r.client(ctx, tx, req.Credentials, "orders.status.manage"); e != nil {
			return c.Order{}, e
		}
		return c.RedactShipping(o), tx.Commit()
	}
	if req.Status == c.Finished && o.Status != c.Paid || req.Status == c.Cancelled && o.Status != c.Paid && o.Status != c.Unpaid {
		return o, c.ErrConflict
	}
	now := r.now().UTC()
	if _, e = r.client(ctx, tx, req.Credentials, "orders.status.manage"); e != nil {
		return o, e
	}
	if o.Status == c.Paid {
		if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, req.Status == c.Cancelled, now); e != nil {
			return o, e
		}
	}
	o.Status = req.Status
	o.UpdatedAt = now
	if e = saveOrder(ctx, tx, o); e != nil {
		return o, e
	}
	kind := "order.finished"
	if o.Status == c.Cancelled {
		kind = "order.cancelled"
	}
	if e = event(ctx, tx, kind, o.ID, o, now); e != nil {
		return o, e
	}
	if _, e = r.client(ctx, tx, req.Credentials, "orders.status.manage"); e != nil {
		return c.Order{}, e
	}
	return c.RedactShipping(o), tx.Commit()
}
func definition(ctx context.Context, tx *sql.Tx, identifier string) (c.Definition, error) {
	return definitionLocked(ctx, tx, identifier, false)
}
func definitionForOrder(ctx context.Context, tx *sql.Tx, identifier string) (c.Definition, error) {
	return definitionLocked(ctx, tx, identifier, true)
}
func definitionLocked(ctx context.Context, tx *sql.Tx, identifier string, stock bool) (c.Definition, error) {
	var d c.Definition
	var b []byte
	// Inspect the optional immutable scope before taking the definition lock.
	if e := tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.definitions WHERE identifier=$1 AND enabled`, identifier).Scan(&b); e != nil {
		return d, notFound(e)
	}
	if e := json.Unmarshal(b, &d); e != nil {
		return d, e
	}
	initialApp := d.AppIdentifier
	if e := lockDefinitionApp(ctx, tx, initialApp); e != nil {
		return d, e
	}
	currentProduct, e := lockDefinitionProduct(ctx, tx, d, stock)
	if e != nil {
		return d, e
	}
	e = tx.QueryRowContext(ctx, `SELECT document,payee_wallet_id::text FROM commerce_v1.definitions WHERE identifier=$1 AND enabled FOR SHARE`, identifier).Scan(&b, &d.PayeeWalletID)
	if e != nil {
		return d, notFound(e)
	}
	payee := d.PayeeWalletID
	d = c.Definition{}
	e = json.Unmarshal(b, &d)
	d.PayeeWalletID = payee
	if e != nil {
		return d, e
	}
	if !sameDefinitionApp(initialApp, d.AppIdentifier) {
		return d, c.ErrConflict
	}
	if e = lockDefinitionApp(ctx, tx, d.AppIdentifier); e != nil {
		return d, e
	}
	if d.ProductID != "" {
		d.ProductTerms, e = recurringTerms(d)
		if e != nil {
			return d, e
		}
		if d.ProductTerms != currentProduct.ProductTerms {
			return d, c.ErrConflict
		}
	}
	if d.Identifier != identifier || d.BasePrice.Sign() <= 0 || d.CycleDurationDays < 1 || d.CycleDurationDays > 366 || d.Currency == "" {
		return d, c.ErrUnavailable
	}
	return d, nil
}
func (r *Repository) Catalog(ctx context.Context) ([]c.Definition, error) {
	if r == nil || r.db == nil {
		return nil, c.ErrUnavailable
	}
	rows, e := r.db.QueryContext(ctx, `SELECT document FROM commerce_v1.definitions d WHERE enabled AND `+catalogVisible+` ORDER BY identifier`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []c.Definition{}
	for rows.Next() {
		var b []byte
		var d c.Definition
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &d)
		}
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func subscriptionRow(ctx context.Context, tx *sql.Tx, idv string, locked bool) (c.Subscription, error) {
	suffix := ""
	if locked {
		suffix = " FOR UPDATE"
	}
	return decode[c.Subscription](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.subscriptions WHERE id=$1`+suffix, idv))
}
func lookupSubscription(ctx context.Context, tx *sql.Tx, actor, identifier string) (c.Subscription, error) {
	// A concrete row selector remains bound to that historical subscription,
	// including an idempotent cancellation after another subscription is created.
	if wallet.ValidID(identifier) {
		s, e := decode[c.Subscription](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.subscriptions WHERE account_id=$1 AND id=$2::uuid FOR UPDATE`, actor, identifier))
		if !errors.Is(e, c.ErrNotFound) {
			return s, e
		}
	}
	return lookupSubscriptionDefinition(ctx, tx, actor, identifier)
}
func lookupSubscriptionDefinition(ctx context.Context, tx *sql.Tx, actor, identifier string) (c.Subscription, error) {
	// Cancelled/expired history cannot shadow the current unpaid or active row.
	// BegunAt is an entitlement schedule, not a creation sequence; queued cycles
	// can start in the future and multiple creations can share the same clock.
	return decode[c.Subscription](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.subscriptions WHERE account_id=$1 AND identifier=$2 ORDER BY (status IN(0,1)) DESC,begun_at DESC,id DESC LIMIT 1 FOR UPDATE`, actor, identifier))
}
func saveSubscription(ctx context.Context, tx *sql.Tx, s c.Subscription) error {
	b, e := jsonBytes(s)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE commerce_v1.subscriptions SET status=$2,begun_at=$3,ended_at=$4,renewal_at=$5,payer_wallet_id=$6,document=$7 WHERE id=$1`, s.ID, s.Status, s.BegunAt, s.EndedAt, s.RenewalAt, s.PaymentDetails.PayerWalletID, b)
	return e
}
func insertSubscription(ctx context.Context, tx *sql.Tx, s c.Subscription, days int, renew bool) error {
	b, e := jsonBytes(s)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.subscriptions(id,account_id,identifier,status,begun_at,ended_at,renewal_at,cycle_days,auto_renew,payer_wallet_id,document) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, s.ID, s.AccountID, s.Identifier, s.Status, s.BegunAt, s.EndedAt, s.RenewalAt, days, renew, s.PaymentDetails.PayerWalletID, b)
	return e
}
func (r *Repository) CreateSubscription(ctx context.Context, actor string, req c.CreateSubscription, key string, noop bool) (c.Subscription, error) {
	var s c.Subscription
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return s, e
	}
	defer tx.Rollback()
	if req.PaymentMethod == "" {
		req.PaymentMethod = "solian.wallet"
	}
	if req.Coupon != nil && req.IsAutoRenewal != nil && *req.IsAutoRenewal {
		return s, c.ErrInvalid
	}
	if req.PaymentMethod != "solian.wallet" || req.IsFreeTrial {
		return s, c.ErrUnavailable
	}
	d, e := definition(ctx, tx, req.Identifier)
	if e != nil {
		return s, e
	}
	allowed := false
	for _, m := range d.AllowedPaymentMethods {
		if m == req.PaymentMethod {
			allowed = true
		}
	}
	if !allowed {
		return s, c.ErrForbidden
	}
	days, price, e := c.CyclePrice(d, req.CycleDurationDays)
	if e != nil {
		return s, e
	}
	consentVersion, e := recurringConsent(ctx, tx, actor, d, 0)
	if e != nil {
		return s, e
	}
	if req.PaymentDetails.Currency != "" && req.PaymentDetails.Currency != d.Currency {
		return s, c.ErrInvalid
	}
	fp, e := fingerprint(req)
	if e != nil {
		return s, e
	}
	prior, e := idem(ctx, tx, "account:"+actor, key, "subscription", fp)
	if e != nil {
		return s, e
	}
	if prior != "" {
		s, e = subscriptionRow(ctx, tx, prior, false)
		if e != nil {
			return s, e
		}
		return s, tx.Commit()
	}
	old, e := lookupSubscriptionDefinition(ctx, tx, actor, req.Identifier)
	if e == nil && (old.Status == c.SubscriptionUnpaid || old.Status == c.SubscriptionActive) {
		if noop && req.Coupon == nil {
			return old, tx.Commit()
		}
		return old, c.ErrConflict
	}
	if e != nil && !errors.Is(e, c.ErrNotFound) {
		return s, e
	}
	payer, e := defaultWallet(ctx, tx, actor, req.PaymentDetails.PayerWalletID)
	if e != nil {
		return s, e
	}
	sid, e := id()
	if e != nil {
		return s, e
	}
	now := r.now().UTC()
	renew := d.AllowInternalWalletRenewal && (req.IsAutoRenewal == nil || *req.IsAutoRenewal)
	if d.ProductID != "" {
		renew = d.AllowInternalWalletRenewal && req.IsAutoRenewal != nil && *req.IsAutoRenewal
	}
	s = c.Subscription{AppIdentifier: d.AppIdentifier, ProductTerms: d.ProductTerms, ConsentVersion: consentVersion, CycleDurationDays: days, IsAutoRenewal: renew, ID: sid, AccountID: actor, Identifier: d.Identifier, GroupIdentifier: d.GroupIdentifier, DisplayName: d.DisplayName, Status: c.SubscriptionUnpaid, PaymentMethod: req.PaymentMethod, PaymentDetails: c.PaymentDetails{Currency: d.Currency, PayerWalletID: &payer}, BasePrice: d.BasePrice, FinalPrice: price, BegunAt: now, EndedAt: now.Add(time.Duration(days) * 24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	if req.Coupon != nil {
		app, err := couponApp(d)
		if err != nil {
			return s, err
		}
		s.Coupon, e = r.quoteCoupon(ctx, tx, actor, app, d.PayeeWalletID, "subscription", d.Currency, *req.Coupon, []string{d.Identifier}, price)
		if e != nil {
			return s, e
		}
		s.FinalPrice = s.Coupon.Net
		s.IsAutoRenewal = false
		renew = false
	}
	if e = insertSubscription(ctx, tx, s, days, renew); e != nil {
		return s, e
	}
	if e = recordIdem(ctx, tx, "account:"+actor, key, "subscription", fp, s.ID); e != nil {
		return s, e
	}
	if e = event(ctx, tx, "subscription.created", s.ID, s, now); e != nil {
		return s, e
	}
	if s.Coupon != nil {
		if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return s, e
		}
		if e = couponIssuer(ctx, tx, s.Coupon.AppID, s.Coupon.PublisherID, s.Coupon.PayeeWalletID); e != nil {
			return s, e
		}
		if !s.Coupon.ExpiresAt.After(r.now().UTC()) {
			return s, c.ErrConflict
		}
	}
	return s, tx.Commit()
}
func (r *Repository) Subscription(ctx context.Context, actor, identifier string) (c.Subscription, error) {
	var s c.Subscription
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return s, e
	}
	defer tx.Rollback()
	s, e = lookupSubscription(ctx, tx, actor, identifier)
	if e != nil {
		return s, e
	}
	s, e = subscriptionAccess(ctx, tx, s, r.now().UTC())
	if e != nil {
		return s, e
	}
	return s, tx.Commit()
}
func (r *Repository) Subscriptions(ctx context.Context, actor string, q c.PageQuery) ([]c.Subscription, int, error) {
	out := []c.Subscription{}
	q = bounds(q)
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	var total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM commerce_v1.subscriptions WHERE account_id=$1`, actor).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT document FROM commerce_v1.subscriptions WHERE account_id=$1 ORDER BY begun_at DESC,id DESC OFFSET $2 LIMIT $3`, actor, q.Offset, q.Take)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		var b []byte
		var s c.Subscription
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &s)
		}
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		out[i], e = subscriptionAccess(ctx, tx, out[i], r.now().UTC())
		if e != nil {
			return nil, 0, e
		}
	}
	return out, total, tx.Commit()
}
func (r *Repository) CancelSubscription(ctx context.Context, actor, identifier string) (c.Subscription, error) {
	var s c.Subscription
	tx, e := r.userTx(ctx, actor, "subscriptions.cancel")
	if e != nil {
		return s, e
	}
	defer tx.Rollback()
	s, e = lookupSubscription(ctx, tx, actor, identifier)
	if e != nil {
		return s, e
	}
	if s.Status == c.SubscriptionCancelled {
		return s, tx.Commit()
	}
	if s.PaymentMethod != "solian.wallet" {
		return s, c.ErrForbidden
	}
	now := r.now().UTC()
	s.Status = c.SubscriptionCancelled
	s.IsAutoRenewal = false
	s.IsActive = false
	s.RenewalAt = nil
	s.UpdatedAt = now
	if now.Before(s.EndedAt) {
		s.EndedAt = now
	}
	if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.subscriptions SET auto_renew=false WHERE id=$1`, s.ID); e != nil {
		return s, e
	}
	if e = saveSubscription(ctx, tx, s); e != nil {
		return s, e
	}
	if e = cancelSubscriptionReservations(ctx, tx, s, now); e != nil {
		return s, e
	}
	if e = event(ctx, tx, "subscription.cancelled", s.ID, s, now); e != nil {
		return s, e
	}
	return s, tx.Commit()
}
func (r *Repository) SubscriptionOrder(ctx context.Context, actor, identifier string) (c.Order, error) {
	var o c.Order
	tx, e := r.userTx(ctx, actor, "subscriptions.order.manage")
	if e != nil {
		return o, e
	}
	defer tx.Rollback()
	s, e := lookupSubscription(ctx, tx, actor, identifier)
	if e != nil {
		return o, e
	}
	if s.Status == c.SubscriptionCancelled || s.Coupon != nil && s.Status != c.SubscriptionUnpaid {
		return o, c.ErrConflict
	}
	d, e := definitionForOrder(ctx, tx, s.Identifier)
	if e != nil {
		return o, e
	}
	if e = subscriptionEconomics(ctx, tx, s, d); e != nil {
		return o, e
	}
	o, e = r.resourceOrder(ctx, tx, actor, d, s.ID, "", s.PaymentDetails.PayerWalletID, s.FinalPrice, s.Coupon)
	if e != nil {
		return o, e
	}
	return o, tx.Commit()
}
func (r *Repository) resourceOrder(ctx context.Context, tx *sql.Tx, actor string, d c.Definition, sub, gift string, payer *string, price wallet.Amount, coupon *c.CouponQuote) (c.Order, error) {
	now := r.now().UTC()
	o, e := decode[c.Order](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.orders WHERE actor_id=$1 AND subscription_id IS NOT DISTINCT FROM $2::uuid AND gift_id IS NOT DISTINCT FROM $3::uuid AND status=0 AND expires_at>$4 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, actor, nullable(sub), nullable(gift), now))
	if e == nil {
		if !sameCoupon(o.Coupon, coupon) {
			return o, c.ErrConflict
		}
		return o, nil
	}
	if !errors.Is(e, c.ErrNotFound) {
		return o, e
	}
	now = r.now().UTC()
	oid, e := id()
	if e != nil {
		return o, e
	}
	product := d.Identifier
	o = c.Order{Coupon: coupon, ID: oid, Status: c.Unpaid, Currency: d.Currency, Amount: price, AppIdentifier: "internal", ProductIdentifier: &product, PayeeWalletID: d.PayeeWalletID, PayerWalletID: payer, Meta: map[string]json.RawMessage{}, Items: []c.OrderItem{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(24 * time.Hour)}
	if sub != "" {
		o.Meta["subscription_id"], _ = json.Marshal(sub)
		o.Meta["subscription_identifier"], _ = json.Marshal(d.Identifier)
	}
	if gift != "" {
		o.AppIdentifier = "gift"
		o.Meta["gift_id"], _ = json.Marshal(gift)
	}
	app := ""
	var stockProduct Product
	var quantity int
	if d.ProductID != "" {
		if sub == "" || gift != "" {
			return o, c.ErrUnavailable
		}
		stockProduct, quantity, e = subscriptionStock(ctx, tx, d, sub, now)
		if e != nil {
			return o, e
		}
		o.Items = []c.OrderItem{{ProductIdentifier: stockProduct.Identifier, Quantity: quantity, UnitPrice: stockProduct.Price, Currency: stockProduct.Currency}}
		captured, err := c.Total(o.Items)
		if err != nil || captured.Cmp(o.GrossAmount()) != 0 || stockProduct.Currency != o.Currency {
			return o, c.ErrConflict
		}
	}
	if d.AppIdentifier != nil && (d.ProductID != "" || coupon != nil) {
		o.AppIdentifier = *d.AppIdentifier
		app = strings.TrimPrefix(*d.AppIdentifier, "developer.app:")
	}
	if coupon != nil {
		if e = r.validateCouponQuote(ctx, tx, actor, o, false); e != nil {
			return o, e
		}
	}
	if e = insertOrder(ctx, tx, o, app, actor, sub, gift); e != nil {
		return o, e
	}
	if d.ProductID != "" {
		e = reserveProduct(ctx, tx, o.ID, stockProduct.ID, stockProduct.Identifier, quantity)
	}
	return o, e
}
func (r *Repository) activateSubscription(ctx context.Context, tx *sql.Tx, sid string, o c.Order, now time.Time) error {
	s, e := subscriptionRow(ctx, tx, sid, true)
	if e != nil {
		return e
	}
	if s.Status == c.SubscriptionCancelled {
		return c.ErrConflict
	}
	var days int
	var renew bool
	e = tx.QueryRowContext(ctx, `SELECT cycle_days,auto_renew FROM commerce_v1.subscriptions WHERE id=$1`, sid).Scan(&days, &renew)
	if e != nil {
		return e
	}
	d, e := definition(ctx, tx, s.Identifier)
	if e != nil {
		return e
	}
	if e = subscriptionEconomics(ctx, tx, s, d); e != nil {
		return e
	}
	if !sameCoupon(s.Coupon, o.Coupon) || s.FinalPrice.Cmp(o.Amount) != 0 || s.PaymentDetails.Currency != o.Currency || o.PayeeWalletID != d.PayeeWalletID {
		return c.ErrConflict
	}
	if s.Status == c.SubscriptionActive && s.EndedAt.After(now) {
		s.EndedAt = s.EndedAt.Add(time.Duration(days) * 24 * time.Hour)
	} else {
		begin, e := groupStart(ctx, tx, s.AccountID, d.GroupIdentifier, d.Identifier, s.ID, now, false)
		if e != nil {
			return e
		}
		s.BegunAt = begin
		s.EndedAt = begin.Add(time.Duration(days) * 24 * time.Hour)
	}

	s.RenewalAt = nil
	s.IsAutoRenewal = renew && d.AllowInternalWalletRenewal
	s.CycleDurationDays = days
	if s.IsAutoRenewal {
		s.RenewalAt = &s.EndedAt
	}
	s.Status = c.SubscriptionActive
	s.IsActive = true
	s.PaymentDetails.OrderID = o.ID
	s.PaymentDetails.PayerWalletID = o.PayerWalletID
	s.UpdatedAt = now
	if e = saveSubscription(ctx, tx, s); e != nil {
		return e
	}
	return event(ctx, tx, "subscription.activated", s.ID, s, now)
}
