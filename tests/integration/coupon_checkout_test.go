// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sync"
	"testing"
	"time"
)

func couponPolicy() c.CouponPolicy {
	return c.CouponPolicy{Type: c.SellerFixedMills, Cancellation: c.RetainCouponUsage}
}
func enableCoupons(t *testing.T, f commerceFixture) commerceFixture {
	t.Helper()
	var ready bool
	if e := f.db.QueryRow(`SELECT to_regclass('commerce_v1.coupon_redemptions') IS NOT NULL`).Scan(&ready); e != nil || !ready {
		t.Fatal("coupon migration not registered in disposable harness", e)
	}
	f.r.WithCouponPolicy(couponPolicy())
	query(t, f.db, `UPDATE owned_custom_apps SET oauth_config=jsonb_set(oauth_config,'{allowed_scopes}',oauth_config->'allowed_scopes'||'["coupons.manage"]'::jsonb) WHERE id=$1`, f.app)
	query(t, f.db, `UPDATE owned_app_secrets SET scopes=scopes||'["coupons.manage"]'::jsonb WHERE id=$1`, f.secretID)
	return f
}
func couponRequest(f commerceFixture, scope string, products []string) c.CreateCoupon {
	now := time.Unix(f.clock.Load(), 0).UTC()
	return c.CreateCoupon{Credentials: f.credentials, Identifier: "SYNTHETIC_SAVE", Terms: c.CouponTerms{Type: "fixed_mills", Scope: scope, Currency: "synthetic-commerce", Discount: json.Number("1.001"), Products: products, Audience: "all", Accounts: []string{}, StartsAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), MaxUses: 100, MaxUsesPerAccount: 100}}
}
func createCoupon(t *testing.T, f commerceFixture, q c.CreateCoupon) c.Coupon {
	t.Helper()
	got, e := f.r.CreateCoupon(f.ctx, q, "coupon-"+q.Identifier)
	if e != nil {
		t.Fatal(e)
	}
	return got
}
func discountedOrder(t *testing.T, f commerceFixture, code, key string) c.Order {
	t.Helper()
	o := commerceOrder(t, f, key, true)
	o, e := f.r.ApplyOrderCoupon(f.ctx, f.actor, o.ID, code, false)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func TestPGCouponCheckoutConservationReplayRefundFreshRepository(t *testing.T) {
	f := enableCoupons(t, newCommerceFixture(t))
	cp := createCoupon(t, f, couponRequest(f, "order", []string{"synthetic-widget"}))
	o := commerceOrder(t, f, "preview-then-pay", true)
	preview, e := f.r.ApplyOrderCoupon(f.ctx, f.actor, o.ID, cp.Identifier, true)
	if e != nil || preview.Amount.String() != "3.246" || preview.Coupon.Gross.String() != "4.247" {
		t.Fatal(preview, e)
	}
	saved, e := f.r.GetOrder(f.ctx, "", o.ID, true)
	if e != nil || saved.Amount.String() != "4.247" || saved.Coupon != nil {
		t.Fatal("preview mutated", saved, e)
	}
	quoted, e := f.r.ApplyOrderCoupon(f.ctx, f.actor, o.ID, cp.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.r.ApplyOrderCoupon(f.ctx, f.actor, o.ID, cp.Identifier, false)
	if e != nil || !reflect.DeepEqual(again.Coupon, quoted.Coupon) {
		t.Fatal("quote replay changed", again, e)
	}
	secondPool, ctx := commercePool(t, f)
	secondPool.WithCouponPolicy(couponPolicy())
	paid, e := secondPool.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID)
	if e != nil || paid.Status != c.Paid || !reflect.DeepEqual(paid.Coupon, quoted.Coupon) {
		t.Fatal(paid, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.754", "3.246", "96.754")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE order_id=$1 AND gross=4.247 AND discount=1.001 AND net=3.246`, o.ID); n != 1 {
		t.Fatal(n)
	}
	// Revocation and policy shutdown do not reinterpret already-paid replay/refund.
	if _, e = f.r.RevokeCoupon(f.ctx, cp.ID, c.RevokeCoupon{Credentials: f.credentials}); e != nil {
		t.Fatal(e)
	}
	secondPool.WithCouponPolicy(c.CouponPolicy{})
	replay, e := secondPool.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID)
	if e != nil || *replay.TransactionID != *paid.TransactionID || !reflect.DeepEqual(replay.Coupon, paid.Coupon) {
		t.Fatal(replay, e)
	}
	public, e := f.r.GetOrder(f.ctx, "", o.ID, true)
	if e != nil || public.Coupon != nil {
		t.Fatal("public coupon audience/code leak", public, e)
	}
	if _, e = secondPool.UpdateOrder(ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	if _, e = secondPool.UpdateOrder(ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 1 {
		t.Fatal("configured retain_usage policy lost redemption", n)
	}
}
func TestPGCouponCapacityConcurrentCheckoutAndFailureRollback(t *testing.T) {
	f := enableCoupons(t, newCommerceFixture(t))
	q := couponRequest(f, "order", []string{"synthetic-widget"})
	q.Terms.MaxUses = 1
	q.Terms.MaxUsesPerAccount = 1
	cp := createCoupon(t, f, q)
	orders := []c.Order{discountedOrder(t, f, cp.ID, "a"), discountedOrder(t, f, cp.ID, "b")}
	// A failed canonical hold must not consume capacity, reserve money or publish events.
	query(t, f.db, `UPDATE wallet_v1.pockets SET amount=0 WHERE wallet_id=$1 AND currency='synthetic-commerce'`, f.wallet.ID)
	if _, e := f.r.PayOrder(f.ctx, f.actor, orders[0].ID, &f.wallet.ID); !errors.Is(e, wallet.ErrInsufficient) {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, orders[0].ID); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `UPDATE wallet_v1.pockets SET amount=100 WHERE wallet_id=$1 AND currency='synthetic-commerce'`, f.wallet.ID)
	// Failure after the redemption insert must roll back usage and the hold.
	query(t, f.db, `ALTER TABLE commerce_v1.outbox ADD CONSTRAINT synthetic_coupon_failure CHECK(aggregate_id<>'`+orders[0].ID+`'::uuid OR event_type<>'commerce.order.paid') NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE commerce_v1.outbox DROP CONSTRAINT IF EXISTS synthetic_coupon_failure`)
	})
	if _, e := f.r.PayOrder(f.ctx, f.actor, orders[0].ID, &f.wallet.ID); e == nil {
		t.Fatal("injected post-redemption failure committed")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 0 {
		t.Fatal("rollback retained usage", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, orders[0].ID); n != 0 {
		t.Fatal("rollback retained wallet hold", n)
	}
	query(t, f.db, `ALTER TABLE commerce_v1.outbox DROP CONSTRAINT synthetic_coupon_failure`)

	var wg sync.WaitGroup
	results := make(chan c.Order, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, e := f.r.PayOrder(f.ctx, f.actor, orders[i%2].ID, &f.wallet.ID)
			if e != nil {
				errs <- e
			} else {
				results <- o
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	paidID := ""
	for o := range results {
		if paidID != "" && o.ID != paidID {
			t.Fatal("single-use coupon spent twice")
		}
		paidID = o.ID
	}
	if paidID == "" {
		t.Fatal("no successful checkout")
	}
	for e := range errs {
		if !errors.Is(e, c.ErrConflict) {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 1 {
		t.Fatal(n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.754", "3.246", "96.754")
	if _, e := f.r.UpdateOrder(f.ctx, paidID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "3.246", "0.000", "3.246")
}
func TestPGCouponBuyerScopeIssuerAndQuoteChanges(t *testing.T) {
	f := enableCoupons(t, newCommerceFixture(t))
	other := enableCoupons(t, newCommerceFixture(t))
	q := couponRequest(f, "order", []string{"synthetic-widget"})
	q.Terms.Audience = "accounts"
	q.Terms.Accounts = []string{f.actor}
	cp := createCoupon(t, f, q)
	o := commerceOrder(t, f, "isolated", true)
	if _, e := other.r.ApplyOrderCoupon(other.ctx, other.actor, o.ID, cp.ID, false); e == nil {
		t.Fatal("foreign coupon audience")
	}
	o, e := f.r.ApplyOrderCoupon(f.ctx, f.actor, o.ID, cp.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.r.ApplyOrderCoupon(other.ctx, other.actor, o.ID, cp.ID, true); e == nil {
		t.Fatal("foreign quote read")
	}
	if _, e = other.r.PayOrder(other.ctx, other.actor, o.ID, &other.wallet.ID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("foreign buyer", e)
	}
	if _, e = other.r.RevokeCoupon(other.ctx, cp.ID, c.RevokeCoupon{Credentials: other.credentials}); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("foreign issuer", e)
	}
	query(t, f.db, `UPDATE commerce_v1.coupons SET document=jsonb_set(document,'{terms,discount}','1.002') WHERE id=$1`, cp.ID)
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("silently repriced quote", e)
	}
	saved, e := f.r.GetOrder(f.ctx, f.actor, o.ID, false)
	if e != nil || saved.Amount.String() != "3.246" || saved.Coupon.Discount.String() != "1.001" {
		t.Fatal("saved quote changed", saved, e)
	}
	// Captured beneficiary must still be the canonical issuer merchant wallet.
	query(t, f.db, `UPDATE commerce_v1.coupons SET document=jsonb_set(document,'{terms,discount}','1.001') WHERE id=$1`, cp.ID)
	query(t, f.db, `UPDATE commerce_v1.merchants SET wallet_id=$2 WHERE app_id=$1`, f.app, f.wallet.ID)
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e == nil {
		t.Fatal("changed beneficiary admitted")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
}
func TestPGCouponRevocationAndExpiryAfterWait(t *testing.T) {
	for _, which := range []string{"coupon", "app", "scope", "expiry"} {
		t.Run(which, func(t *testing.T) {
			f := enableCoupons(t, newCommerceFixture(t))
			cp := createCoupon(t, f, couponRequest(f, "order", []string{"synthetic-widget"}))
			o := discountedOrder(t, f, cp.ID, "wait")
			blocker, e := f.db.BeginTx(f.ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			table := "commerce_v1.coupons"
			sql := `UPDATE commerce_v1.coupons SET enabled=false WHERE id=$1`
			arg := cp.ID
			if which == "app" {
				table = "owned_custom_apps"
				sql = `UPDATE owned_custom_apps SET status=2 WHERE id=$1`
				arg = f.app
			}
			if which == "scope" {
				table = "owned_custom_apps"
				sql = `UPDATE owned_custom_apps SET oauth_config=jsonb_set(oauth_config,'{allowed_scopes}',(oauth_config->'allowed_scopes')-'coupons.manage') WHERE id=$1`
				arg = f.app
			}
			if which == "expiry" {
				table = "wallet_v1.wallets"
				sql = `SELECT id FROM wallet_v1.wallets WHERE id=$1 FOR UPDATE`
				arg = f.wallet.ID
			}
			if _, e = blocker.Exec(sql, arg); e != nil {
				t.Fatal(e)
			}
			result := make(chan error, 1)
			go func() { _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); result <- e }()
			waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, table)
			if which == "expiry" {
				f.clock.Store(cp.Terms.ExpiresAt.Unix())
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-result:
				if e == nil {
					t.Fatal("invalid coupon/app after lock wait")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("checkout did not unblock")
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 0 {
				t.Fatal("failed checkout consumed coupon", n)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 0 {
				t.Fatal("failed checkout kept hold", n)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
		})
	}
}
func TestPGCouponSubscriptionGiftFirstPurchase(t *testing.T) {
	for _, kind := range []string{"subscription", "gift"} {
		t.Run(kind, func(t *testing.T) {
			f := enableCoupons(t, newCommerceFixture(t))
			ref := "developer.app:" + f.app
			raw, _ := json.Marshal(ref)
			query(t, f.db, `UPDATE commerce_v1.definitions SET document=jsonb_set(document,'{app_identifier}',$2::jsonb) WHERE identifier=$1`, f.plan, raw)
			cp := createCoupon(t, f, couponRequest(f, kind, []string{f.plan}))
			var o c.Order
			var e error
			if kind == "subscription" {
				yes := true
				if _, e = f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan, Coupon: &cp.Identifier, IsAutoRenewal: &yes}, "auto", false); !errors.Is(e, c.ErrInvalid) {
					t.Fatal("coupon standing charge accepted", e)
				}
				s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan, Coupon: &cp.Identifier}, "sub", false)
				if e != nil || s.FinalPrice.String() != "2.332" || s.IsAutoRenewal {
					t.Fatal(s, e)
				}
				o, e = f.r.SubscriptionOrder(f.ctx, f.actor, s.ID)
				if e != nil {
					t.Fatal(e)
				}
			} else {
				g, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet", Coupon: &cp.Identifier}, "gift")
				if e != nil || g.FinalPrice.String() != "2.332" {
					t.Fatal(g, e)
				}
				o, e = f.r.GiftOrder(f.ctx, f.actor, g.ID)
				if e != nil {
					t.Fatal(e)
				}
			}
			if o.Coupon == nil || o.Amount.String() != "2.332" {
				t.Fatal(o)
			}
			paid, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "subscription" {
				if paid.Status != c.Finished {
					t.Fatal(paid)
				}
				if _, e = f.r.SubscriptionOrder(f.ctx, f.actor, f.plan); !errors.Is(e, c.ErrConflict) {
					t.Fatal("first-purchase coupon reused for manual renewal", e)
				}
				walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "2.332", "0.000", "2.332")
			} else {
				var gid string
				if e = json.Unmarshal(o.Meta["gift_id"], &gid); e != nil {
					t.Fatal(e)
				}
				if _, e = f.r.GiftAction(f.ctx, f.actor, gid, false); e != nil {
					t.Fatal(e)
				}
				walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 1 {
				t.Fatal(n)
			}
		})
	}
}
func TestPGCouponDisabledAndUnknownPolicies(t *testing.T) {
	f := newCommerceFixture(t)
	q := couponRequest(f, "order", []string{"synthetic-widget"})
	for _, policy := range []c.CouponPolicy{{}, {Type: c.SellerFixedMills}, {Type: c.SellerFixedMills, Cancellation: "restore"}, {Type: "legacy", Cancellation: c.RetainCouponUsage}} {
		f.r.WithCouponPolicy(policy)
		if _, e := f.r.CreateCoupon(f.ctx, q, ""); !errors.Is(e, c.ErrUnavailable) {
			t.Fatal(policy, e)
		}
	}
	f = enableCoupons(t, f)
	if _, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan, Coupon: stringPointer("SYNTHETIC")}, "platform", false); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal("implicit platform issuer", e)
	}
	if _, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "paddle", Coupon: stringPointer("SYNTHETIC")}, "provider"); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal("unverified provider coupon", e)
	}
}

func TestPGCouponGiftRedemptionPreservesFirstCyclePolicy(t *testing.T) {
	f := enableCoupons(t, newCommerceFixture(t))
	buyer := newCommerceFixture(t)
	ref := "developer.app:" + f.app
	raw, _ := json.Marshal(ref)
	query(t, f.db, `UPDATE commerce_v1.definitions SET document=jsonb_set(document,'{app_identifier}',$2::jsonb) WHERE identifier=$1`, f.plan, raw)
	cp := createCoupon(t, f, couponRequest(f, "gift", []string{f.plan}))
	g, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet", Coupon: &cp.ID, RecipientID: &buyer.actor}, "gift-redeem")
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.GiftOrder(f.ctx, f.actor, g.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.RevokeCoupon(f.ctx, cp.ID, c.RevokeCoupon{Credentials: f.credentials}); e != nil {
		t.Fatal(e)
	}
	// Already purchased gifts honor their captured net and do not spend coupon again.
	redeemed, e := buyer.r.RedeemGift(buyer.ctx, buyer.actor, g.GiftCode)
	if e != nil || redeemed.Subscription.Coupon == nil || redeemed.Subscription.FinalPrice.String() != "2.332" {
		t.Fatal(redeemed, e)
	}
	replay, e := buyer.r.RedeemGift(buyer.ctx, buyer.actor, g.GiftCode)
	if e != nil || replay.Subscription.ID != redeemed.Subscription.ID {
		t.Fatal(replay, e)
	}
	if _, e = buyer.r.SubscriptionOrder(buyer.ctx, buyer.actor, redeemed.Subscription.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("gift coupon became a permanent renewal price", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "2.332", "0.000", "2.332")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 1 {
		t.Fatal(n)
	}
}

func TestPGCouponAppProductSubscriptionAndCheckout(t *testing.T) {
	for _, route := range []string{"order", "subscription"} {
		t.Run(route, func(t *testing.T) {
			f := enableCoupons(t, newRecurringProductFixture(t))
			d := recurringDefinition(t, f)
			products := []string{"synthetic-widget"}
			if route == "subscription" {
				products = []string{d.Identifier}
			}
			cp := createCoupon(t, f, couponRequest(f, route, products))
			var o c.Order
			var e error
			if route == "order" {
				o = recurringOrder(t, f, "coupon-recurring")
				o, e = f.r.ApplyOrderCoupon(f.ctx, f.actor, o.ID, cp.ID, false)
			} else {
				s, x := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier, Coupon: &cp.Identifier}, "coupon-recurring", false)
				if x != nil {
					t.Fatal(x)
				}
				o, e = f.r.SubscriptionOrder(f.ctx, f.actor, s.ID)
			}
			if e != nil || o.Amount.String() != "1.122" || o.Coupon.Gross.String() != "2.123" {
				t.Fatal(o, e)
			}
			paid, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
			if e != nil || paid.Status != c.Finished {
				t.Fatal(paid, e)
			}
			s, e := f.r.Subscription(f.ctx, f.actor, d.Identifier)
			if e != nil || s.Coupon == nil || s.FinalPrice.String() != "1.122" || s.IsAutoRenewal {
				t.Fatal(s, e)
			}
			if _, e = f.r.SubscriptionOrder(f.ctx, f.actor, s.ID); !errors.Is(e, c.ErrConflict) {
				t.Fatal("discounted manual renewal", e)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "1.122", "0.000", "1.122")
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 1 {
				t.Fatal(n)
			}
		})
	}
}

func TestPGCouponIssuerDisableAfterBuyerWalletWait(t *testing.T) {
	seller := enableCoupons(t, newCommerceFixture(t))
	buyer := newCommerceFixture(t)
	buyer.r.WithCouponPolicy(couponPolicy())
	cp := createCoupon(t, seller, couponRequest(seller, "order", []string{"synthetic-widget"}))
	o := commerceOrder(t, seller, "owner-wait", true)
	o, e := buyer.r.ApplyOrderCoupon(buyer.ctx, buyer.actor, o.ID, cp.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	blocker, pid := atomicBlocker(t, buyer.atomicAuthorityFixture, "wallet_v1.wallets", buyer.wallet.ID)
	defer blocker.Rollback()
	result := make(chan error, 1)
	go func() { _, e := buyer.r.PayOrder(buyer.ctx, buyer.actor, o.ID, &buyer.wallet.ID); result <- e }()
	waitAtomicBlocked(t, buyer.atomicAuthorityFixture, pid, "wallet_v1.wallets")
	// Coupon checkout never locks the seller account after locking the buyer.
	// This must commit while the buyer waits and then invalidate admission.
	query(t, seller.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, seller.actor)
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-result:
		if e == nil {
			t.Fatal("disabled issuer completed buyer's checkout")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("checkout did not unblock")
	}
	if n := commerceCount(t, buyer.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE order_id=$1`, o.ID); n != 0 {
		t.Fatal(n)
	}
	walletAssertBalance(t, walletpg.New(buyer.db), buyer.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
}

func TestPGCouponPerBuyerUsageIsolatedFromGlobalCapacity(t *testing.T) {
	seller := enableCoupons(t, newCommerceFixture(t))
	other := newCommerceFixture(t)
	other.r.WithCouponPolicy(couponPolicy())
	q := couponRequest(seller, "order", []string{"synthetic-widget"})
	q.Terms.MaxUses = 3
	q.Terms.MaxUsesPerAccount = 1
	cp := createCoupon(t, seller, q)
	first := discountedOrder(t, seller, cp.ID, "first-buyer")
	second := discountedOrder(t, seller, cp.ID, "same-buyer")
	third := commerceOrder(t, seller, "other-buyer", true)
	third, e := other.r.ApplyOrderCoupon(other.ctx, other.actor, third.ID, cp.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = seller.r.PayOrder(seller.ctx, seller.actor, first.ID, &seller.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = seller.r.PayOrder(seller.ctx, seller.actor, second.ID, &seller.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("per-buyer limit bypass", e)
	}
	if _, e = other.r.PayOrder(other.ctx, other.actor, third.ID, &other.wallet.ID); e != nil {
		t.Fatal("foreign buyer lost remaining global capacity", e)
	}
	if n := commerceCount(t, seller.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 2 {
		t.Fatal(n)
	}
	if _, e = seller.r.UpdateOrder(seller.ctx, first.ID, c.UpdateStatus{Credentials: seller.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	if _, e = seller.r.PayOrder(seller.ctx, seller.actor, second.ID, &seller.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("cancellation restored retained per-buyer usage", e)
	}
	walletAssertBalance(t, walletpg.New(seller.db), seller.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
}

func TestPGCouponRealmIssuerAuthority(t *testing.T) {
	for _, state := range []string{"healthy", "deleted_realm", "disabled_owner", "active_punishment"} {
		t.Run(state, func(t *testing.T) {
			seller := enableCoupons(t, newCommerceFixture(t))
			buyer := newCommerceFixture(t)
			buyer.r.WithCouponPolicy(couponPolicy())
			realm := authorityUUID(t)
			query(t, seller.db, `INSERT INTO owned_realms(id,slug,name,account_id,created_at,updated_at) VALUES($1,$2,'Synthetic coupon realm',$3,now(),now())`, realm, "coupon-"+realm, seller.actor)
			query(t, seller.db, `UPDATE owned_publishers SET type=1,account_id=NULL,realm_id=$2 WHERE id=$1`, seller.publisher, realm)
			query(t, seller.db, `UPDATE wallet_v1.wallets SET account_id=NULL,realm_id=$2 WHERE id=$1`, seller.payee.ID, realm)
			cp := createCoupon(t, seller, couponRequest(seller, "order", []string{"synthetic-widget"}))
			o := commerceOrder(t, seller, "realm-coupon", true)
			o, e := buyer.r.ApplyOrderCoupon(buyer.ctx, buyer.actor, o.ID, cp.ID, false)
			if e != nil {
				t.Fatal(e)
			}
			switch state {
			case "deleted_realm":
				query(t, seller.db, `UPDATE owned_realms SET deleted_at=now() WHERE id=$1`, realm)
			case "disabled_owner":
				query(t, seller.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, seller.actor)
			case "active_punishment":
				query(t, seller.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at) VALUES($1,'disable_account',clock_timestamp())`, seller.actor)
			}
			_, e = buyer.r.PayOrder(buyer.ctx, buyer.actor, o.ID, &buyer.wallet.ID)
			if state == "healthy" {
				if e != nil {
					t.Fatal(e)
				}
			} else {
				if e == nil {
					t.Fatal("invalid realm issuer charged")
				}
				if n := commerceCount(t, seller.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE order_id=$1`, o.ID); n != 0 {
					t.Fatal(n)
				}
			}
		})
	}
}
