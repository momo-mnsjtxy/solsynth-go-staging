// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/walletpg"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
)

func TestPGCouponBackendHTTPPolicyAndBinding(t *testing.T) {
	f := enableCoupons(t, newCommerceFixture(t))
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	cfg := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, CommerceCouponPolicy: couponPolicy()}
	app, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	q := couponRequest(f, "order", []string{"synthetic-widget"})
	cp := backendDecode[c.Coupon](t, backendJSONCall(t, app, "POST", "/wallet/coupons", "", q, 200))
	o := commerceOrder(t, f, "http", true)
	path := "/wallet/orders/" + o.ID
	backendJSONCall(t, app, "POST", path+"/coupon", "", map[string]any{"coupon": cp.ID}, 401)
	backendJSONCall(t, app, "POST", path+"/coupon", f.token, map[string]any{}, 400)
	backendJSONCall(t, app, "POST", path+"/coupon", f.token, map[string]any{"coupon": cp.ID, "amount": 0.001}, 400)
	quoted := backendDecode[c.Order](t, backendJSONCall(t, app, "POST", path+"/coupon", f.token, map[string]any{"coupon": cp.ID}, 200))
	if quoted.Amount.String() != "3.246" {
		t.Fatal(quoted)
	}
	// Canonical payment policy is still required; coupon possession is not a payment authorization.
	backendJSONCall(t, app, "POST", path+"/pay", f.token, map[string]any{"payer_wallet_id": f.wallet.ID}, 503)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at) VALUES($1,false,now())`, f.actor)
	paid := backendDecode[c.Order](t, backendJSONCall(t, app, "POST", path+"/pay", f.token, map[string]any{"payer_wallet_id": f.wallet.ID}, 200))
	if paid.Amount.String() != "3.246" || paid.Coupon == nil {
		t.Fatal("omitting coupon from pay bypassed server quote", paid)
	}
	backendJSONCall(t, app, "POST", "/wallet/coupons/"+cp.ID+"/revoke", "", c.RevokeCoupon{Credentials: f.credentials}, 200)
	replay := backendDecode[c.Order](t, backendJSONCall(t, app, "POST", path+"/pay", f.token, map[string]any{"payer_wallet_id": f.wallet.ID}, 200))
	if *replay.TransactionID != *paid.TransactionID {
		t.Fatal(replay)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.754", "3.246", "96.754")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE order_id=$1`, o.ID); n != 1 {
		t.Fatal(n)
	}
}
