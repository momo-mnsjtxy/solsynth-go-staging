// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"solsynth.local/go-migration/internal/walletpg"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
	"time"
)

func couponScopedDefinition(t *testing.T, f commerceFixture) {
	t.Helper()
	raw, _ := json.Marshal("developer.app:" + f.app)
	query(t, f.db, `UPDATE commerce_v1.definitions SET document=jsonb_set(document,'{app_identifier}',$2::jsonb) WHERE identifier=$1`, f.plan, raw)
}

func couponAssertUnpaid(t *testing.T, f commerceFixture, o c.Order) {
	t.Helper()
	got, e := f.r.GetOrder(f.ctx, f.actor, o.ID, false)
	if e != nil || got.Status != c.Unpaid || got.Amount.Cmp(o.Amount) != 0 || !reflect.DeepEqual(got.Coupon, o.Coupon) {
		t.Fatal("rejected payment mutated the saved quote", got, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE order_id=$1`, o.ID); n != 0 {
		t.Fatal("rejected payment consumed coupon", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 0 {
		t.Fatal("rejected payment retained a hold", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.order.paid'`, o.ID); n != 0 {
		t.Fatal("rejected payment published success", n)
	}
}

func TestPGCouponCatalogChangesRejectPayment(t *testing.T) {
	for _, mutation := range []struct{ name, path, value string }{
		{"price", "{price}", "2.1240"},
		{"currency", "{currency}", `"different-synthetic-currency"`},
		{"policy", "{fulfillment}", `{"is_address_required":true,"required_scopes":[]}`},
		{"version", "{updated_at}", `"2020-01-01T00:00:00Z"`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := enableCoupons(t, newCommerceFixture(t))
			cp := createCoupon(t, f, couponRequest(f, "order", []string{"synthetic-widget"}))
			o := discountedOrder(t, f, cp.ID, "coupon-catalog-snapshot")
			ordinary := commerceOrder(t, f, "ordinary-captured-price", true)
			if o.Coupon.CheckoutTermsHash == "" {
				t.Fatal("missing catalog snapshot")
			}
			query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,$2::text[],$3::jsonb) WHERE id=$1`, f.product, mutation.path, mutation.value)
			if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
				t.Fatal("changed coupon checkout catalog admitted", e)
			}
			couponAssertUnpaid(t, f, o)
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
			// Non-coupon orders intentionally preserve their original captured terms.
			paid, e := f.r.PayOrder(f.ctx, f.actor, ordinary.ID, &f.wallet.ID)
			if e != nil || paid.Amount.Cmp(ordinary.Amount) != 0 || paid.PayeeWalletID != ordinary.PayeeWalletID {
				t.Fatal("coupon policy changed ordinary checkout", paid, e)
			}
		})
	}
	// A coupon must not attach a new catalog price to already captured old items.
	t.Run("changed before coupon application", func(t *testing.T) {
		f := enableCoupons(t, newCommerceFixture(t))
		cp := createCoupon(t, f, couponRequest(f, "order", []string{"synthetic-widget"}))
		o := commerceOrder(t, f, "before-coupon", true)
		query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{price}','2.1240') WHERE id=$1`, f.product)
		if _, e := f.r.ApplyOrderCoupon(f.ctx, f.actor, o.ID, cp.ID, false); !errors.Is(e, c.ErrConflict) {
			t.Fatal("old order silently quoted against new price", e)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders WHERE id=$1 AND actor_id IS NULL AND document->'coupon' IS NULL AND amount=4.247`, o.ID); n != 1 {
			t.Fatal("failed quote reserved or changed order", n)
		}
	})
	t.Run("changed during product lock wait", func(t *testing.T) {
		f := enableCoupons(t, newCommerceFixture(t))
		cp := createCoupon(t, f, couponRequest(f, "order", []string{"synthetic-widget"}))
		o := discountedOrder(t, f, cp.ID, "product-wait")
		blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_app_products", f.product)
		defer blocker.Rollback()
		if _, e := blocker.Exec(`UPDATE owned_app_products SET document=jsonb_set(document,'{price}','2.1240') WHERE id=$1`, f.product); e != nil {
			t.Fatal(e)
		}
		result := make(chan error, 1)
		go func() { _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); result <- e }()
		waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_app_products")
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		select {
		case e := <-result:
			if !errors.Is(e, c.ErrConflict) {
				t.Fatal("stale product snapshot survived wait", e)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("checkout did not unblock")
		}
		couponAssertUnpaid(t, f, o)
	})
}

func TestPGCouponAppSubscriptionVersionChangeRejectsPayment(t *testing.T) {
	f := enableCoupons(t, newRecurringProductFixture(t))
	d := recurringDefinition(t, f)
	cp := createCoupon(t, f, couponRequest(f, "subscription", []string{d.Identifier}))
	s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier, Coupon: &cp.ID}, "app-version", false)
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{updated_at}','"2020-01-01T00:00:00Z"') WHERE id=$1`, f.product)
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("app subscription source version changed without rejection", e)
	}
	couponAssertUnpaid(t, f, o)
}

func TestPGCouponQuoteReplayRechecksIssuerAfterMerchantWait(t *testing.T) {
	seller := enableCoupons(t, newCommerceFixture(t))
	buyer := newCommerceFixture(t)
	buyer.r.WithCouponPolicy(couponPolicy())
	cp := createCoupon(t, seller, couponRequest(seller, "order", []string{"synthetic-widget"}))
	o := commerceOrder(t, seller, "quote-replay-owner-wait", true)
	o, e := buyer.r.ApplyOrderCoupon(buyer.ctx, buyer.actor, o.ID, cp.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	blocker, e := seller.db.BeginTx(seller.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var pid int
	if e = blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.Exec(`SELECT app_id FROM commerce_v1.merchants WHERE app_id=$1 FOR UPDATE`, seller.app); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, err := buyer.r.ApplyOrderCoupon(buyer.ctx, buyer.actor, o.ID, cp.ID, false); result <- err }()
	waitAtomicBlocked(t, buyer.atomicAuthorityFixture, pid, "commerce_v1.merchants")
	query(t, seller.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, seller.actor)
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-result:
		if !errors.Is(e, c.ErrForbidden) {
			t.Fatal("quote replay retained issuer state from before merchant wait", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("quote replay did not unblock")
	}
	couponAssertUnpaid(t, buyer, o)
}

func TestPGCouponConfiguredDefinitionChangesRejectPayment(t *testing.T) {
	for _, scope := range []string{"subscription", "gift"} {
		for _, phase := range []string{"before order", "before payment"} {
			for _, mutation := range []struct{ name, path, value string }{
				{"price", "{base_price}", "4.333"},
				{"duration", "{cycle_duration_days}", "2"},
				{"policy", "{allowed_payment_methods}", `["paddle"]`},
			} {
				t.Run(scope+"/"+phase+"/"+mutation.name, func(t *testing.T) {
					f := enableCoupons(t, newCommerceFixture(t))
					couponScopedDefinition(t, f)
					cp := createCoupon(t, f, couponRequest(f, scope, []string{f.plan}))
					var createOrder func() (c.Order, error)
					if scope == "subscription" {
						s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan, Coupon: &cp.ID}, "definition-sub", false)
						if e != nil {
							t.Fatal(e)
						}
						createOrder = func() (c.Order, error) { return f.r.SubscriptionOrder(f.ctx, f.actor, s.ID) }
					} else {
						g, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet", Coupon: &cp.ID}, "definition-gift")
						if e != nil {
							t.Fatal(e)
						}
						createOrder = func() (c.Order, error) { return f.r.GiftOrder(f.ctx, f.actor, g.ID) }
					}
					var o c.Order
					var e error
					if phase == "before payment" {
						o, e = createOrder()
						if e != nil {
							t.Fatal(e)
						}
					}
					query(t, f.db, `UPDATE commerce_v1.definitions SET document=jsonb_set(document,$2::text[],$3::jsonb) WHERE identifier=$1`, f.plan, mutation.path, mutation.value)
					if phase == "before order" {
						if _, e = createOrder(); !errors.Is(e, c.ErrConflict) {
							t.Fatal("changed coupon definition admitted", e)
						}
					} else {
						if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
							t.Fatal("changed coupon definition paid", e)
						}
						couponAssertUnpaid(t, f, o)
					}
					if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 0 {
						t.Fatal("definition mismatch consumed usage", n)
					}
					walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
				})
			}
		}
	}
}

func TestPGCouponGiftIssuerCannotSettleEscrow(t *testing.T) {
	for _, finish := range []string{"cancel", "redeem"} {
		t.Run(finish, func(t *testing.T) {
			f := enableCoupons(t, newCommerceFixture(t))
			recipient := newCommerceFixture(t)
			couponScopedDefinition(t, f)
			cp := createCoupon(t, f, couponRequest(f, "gift", []string{f.plan}))
			g, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet", Coupon: &cp.ID, RecipientID: &recipient.actor}, "gift-status-bypass")
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
			events := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id IN($1,$2)`, o.ID, g.ID)
			for _, status := range []c.OrderStatus{c.Finished, c.Cancelled} {
				if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: status}); !errors.Is(e, c.ErrForbidden) {
					t.Fatal("coupon issuer took gift escrow authority", status, e)
				}
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id IN($1,$2)`, o.ID, g.ID); n != events {
				t.Fatal("denied merchant transition emitted an event", n, events)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders o JOIN commerce_v1.gifts g ON g.id=o.gift_id WHERE o.id=$1 AND o.status=1 AND g.status=1`, o.ID); n != 1 {
				t.Fatal("denied merchant transition changed gift state", n)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "97.668", "2.332", "97.668")
			walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
			if finish == "cancel" {
				if _, e = f.r.GiftAction(f.ctx, f.actor, g.ID, false); e != nil {
					t.Fatal(e)
				}
				walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
			} else {
				if _, e = recipient.r.RedeemGift(recipient.ctx, recipient.actor, g.GiftCode); e != nil {
					t.Fatal(e)
				}
				walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "2.332", "0.000", "2.332")
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, cp.ID); n != 1 {
				t.Fatal("gift lifecycle changed coupon usage", n)
			}
		})
	}
}
