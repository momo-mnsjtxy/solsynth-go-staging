// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sync"
	"testing"
	"time"
)

func newRecurringProductFixture(t *testing.T) commerceFixture {
	t.Helper()
	f := newCommerceFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "subscriptions.*"}, {Pattern: "wallets.*"}, {Pattern: "developers.*"}, {Pattern: apps.BoardScope}}, nil); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_custom_apps SET oauth_config=jsonb_set(oauth_config,'{allowed_scopes}',oauth_config->'allowed_scopes'||'["accounts.profile.board"]'::jsonb) WHERE id=$1::uuid`, f.app)
	query(t, f.db, `INSERT INTO owned_app_consents(account_id,app_id,scopes,expires_at,created_at,updated_at) VALUES($1::uuid,$2::uuid,'["accounts.profile.board"]',now()+interval '1 hour',now(),now())`, f.actor, f.app)
	query(t, f.db, `UPDATE owned_app_products SET document=document||'{"recurrence":"monthly","state":{"is_enabled":true,"stock_mode":"unlimited"},"fulfillment":{"required_scopes":["accounts.profile.board"],"is_address_required":false}}'::jsonb WHERE id=$1::uuid`, f.product)
	recurringSync(t, f)
	return f
}
func recurringSync(t *testing.T, f commerceFixture) {
	t.Helper()
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var raw []byte
	if e = tx.QueryRow(`SELECT document FROM owned_app_products WHERE id=$1::uuid FOR UPDATE`, f.product).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var p apps.Product
	if e = json.Unmarshal(raw, &p); e != nil {
		t.Fatal(e)
	}
	if e = f.r.SyncProduct(f.ctx, tx, p, false); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}
func recurringOrder(t *testing.T, f commerceFixture, key string) c.Order {
	t.Helper()
	o, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}}, key)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func recurringDefinition(t *testing.T, f commerceFixture) c.Definition {
	t.Helper()
	defs, e := f.r.Catalog(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range defs {
		if d.ProductID == f.product {
			return d
		}
	}
	t.Fatal("recurring product missing from canonical catalog")
	return c.Definition{}
}

func TestPGRecurringProductCheckoutAtomicAndIsolation(t *testing.T) {
	f := newRecurringProductFixture(t)
	d := recurringDefinition(t, f)
	if d.Identifier != "app-product:"+f.product || d.CycleDurationDays != 30 || d.BasePrice.String() != "2.123" {
		t.Fatal(d)
	}
	o := recurringOrder(t, f, "recurring")
	public, e := f.r.GetOrder(f.ctx, "", o.ID, true)
	if e != nil || public.Meta["app_subscription"] == nil || public.PayerWalletID != nil {
		t.Fatal(public, e)
	}
	query(t, f.db, `ALTER TABLE commerce_v1.outbox ADD CONSTRAINT synthetic_recurring_failure CHECK(aggregate_id<>'`+o.ID+`'::uuid OR event_type<>'commerce.order.paid') NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE commerce_v1.outbox DROP CONSTRAINT IF EXISTS synthetic_recurring_failure`)
	})
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e == nil {
		t.Fatal("outbox failure did not roll back")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.subscriptions WHERE account_id=$1 AND identifier=$2`, f.actor, d.Identifier); n != 0 {
		t.Fatal("rolled-back grant survived", n)
	}
	query(t, f.db, `ALTER TABLE commerce_v1.outbox DROP CONSTRAINT synthetic_recurring_failure`)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		r, ctx := commercePool(t, f)
		wg.Add(1)
		go func() { defer wg.Done(); _, x := r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID); errs <- x }()
	}
	wg.Wait()
	close(errs)
	for x := range errs {
		if x != nil {
			t.Fatal(x)
		}
	}
	paid, e := f.r.GetOrder(f.ctx, f.actor, o.ID, false)
	if e != nil || paid.Status != c.Finished || paid.TransactionID == nil {
		t.Fatal(paid, e)
	}
	s, e := f.r.Subscription(f.ctx, f.actor, d.Identifier)
	if e != nil || s.Status != c.SubscriptionActive || s.IsAutoRenewal || s.RenewalAt != nil || s.ProductTerms == "" || s.ConsentVersion < 1 || s.CycleDurationDays != 30 {
		t.Fatal(s, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.subscriptions WHERE account_id=$1 AND identifier=$2`, f.actor, d.Identifier); n != 1 {
		t.Fatal("duplicate grants", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 1 {
		t.Fatal("duplicate money", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "97.877", "2.123", "97.877")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	if released, settleErr := merchantRepo(t, f).Settle(f.ctx, f.actor, f.publisher); settleErr != nil || len(released) != 1 {
		t.Fatal("explicit recurring settlement", released, settleErr)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "97.877", "0.000", "97.877")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "2.123", "0.000", "2.123")
	if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("paid entitlement refunded", e)
	}
	other := newRecurringProductFixture(t)
	if recurringDefinition(t, other).Identifier == d.Identifier {
		t.Fatal("SKU from another app collided")
	}
	if _, e = other.r.Subscription(other.ctx, other.actor, s.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("foreign subscription exposed", e)
	}
	if _, e = f.r.CancelSubscription(f.ctx, f.actor, s.ID); e != nil {
		t.Fatal("authored client UUID cancellation failed", e)
	}
	s, e = f.r.Subscription(f.ctx, f.actor, s.ID)
	if e != nil || s.IsActive || s.Status != c.SubscriptionCancelled {
		t.Fatal(s, e)
	}
}

func TestPGRecurringProductRenewalEconomicsAndRevocation(t *testing.T) {
	for _, change := range []string{"price", "currency", "payee", "cycle", "consent", "disabled"} {
		t.Run(change, func(t *testing.T) {
			f := newRecurringProductFixture(t)
			d := recurringDefinition(t, f)
			yes := true
			days := 60
			s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier, PaymentDetails: c.PaymentDetails{PayerWalletID: &f.wallet.ID}, CycleDurationDays: &days, IsAutoRenewal: &yes}, "buyer-opt-in", false)
			if e != nil || s.FinalPrice.String() != "4.247" || !s.IsAutoRenewal {
				t.Fatal(s, e)
			}
			o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.Identifier)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
				t.Fatal(e)
			}
			s, e = f.r.Subscription(f.ctx, f.actor, s.Identifier)
			if e != nil {
				t.Fatal(e)
			}
			dueMaintenanceSubscription(t, f, s, f.config.Now().Add(-time.Second))
			if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
				t.Fatal("first renewal", e)
			}
			s, e = f.r.Subscription(f.ctx, f.actor, s.Identifier)
			if e != nil || s.Status != c.SubscriptionActive || s.PaymentDetails.OrderID == o.ID {
				t.Fatal("renewal not recorded", s, e)
			}
			if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
				t.Fatal(e)
			}
			if released, settleErr := merchantRepo(t, f).Settle(f.ctx, f.actor, f.publisher); settleErr != nil || len(released) != 2 {
				t.Fatal("explicit initial and renewal settlement", released, settleErr)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "91.506", "0.000", "91.506")
			switch change {
			case "price":
				query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{price}','9') WHERE id=$1`, f.product)
			case "currency":
				query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{currency}','"other-synthetic"') WHERE id=$1`, f.product)
			case "cycle":
				query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{recurrence}','"weekly"') WHERE id=$1`, f.product)
			case "payee":
				other := walletCreate(t, walletpg.New(f.db), f.actor)
				query(t, f.db, `UPDATE commerce_v1.merchants SET wallet_id=$2 WHERE app_id=$1`, f.app, other.ID)
			case "consent":
				query(t, f.db, `UPDATE owned_app_consents SET revoked_at=now(),version=version+1 WHERE account_id=$1 AND app_id=$2`, f.actor, f.app)
			case "disabled":
				query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{state,is_enabled}','false') WHERE id=$1`, f.product)
			}
			if change != "consent" {
				recurringSync(t, f)
			}
			dueMaintenanceSubscription(t, f, s, f.config.Now().Add(-time.Second))
			if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
				t.Fatal("changed renewal should expire", e)
			}
			s, e = f.r.Subscription(f.ctx, f.actor, s.Identifier)
			if e != nil || s.Status != c.SubscriptionExpired || s.RenewalAt != nil {
				t.Fatal(s, e)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "91.506", "0.000", "91.506")
		})
	}
}

func TestPGRecurringProductConsentWaitAndSnapshot(t *testing.T) {
	f := newRecurringProductFixture(t)
	o := recurringOrder(t, f, "consent-wait")
	block, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback()
	var pid int
	if e = block.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = block.Exec(`SELECT version FROM owned_app_consents WHERE account_id=$1 AND app_id=$2 FOR UPDATE`, f.actor, f.app); e != nil {
		t.Fatal(e)
	}
	r, ctx := commercePool(t, f)
	done := make(chan error, 1)
	go func() { _, x := r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID); done <- x }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_app_consents")
	if _, e = block.Exec(`UPDATE owned_app_consents SET revoked_at=now(),version=version+1 WHERE account_id=$1 AND app_id=$2`, f.actor, f.app); e != nil {
		t.Fatal(e)
	}
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revoked while waiting", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	query(t, f.db, `UPDATE owned_app_consents SET revoked_at=NULL,version=version+1 WHERE account_id=$1 AND app_id=$2`, f.actor, f.app)
	query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{price}','5') WHERE id=$1`, f.product)
	recurringSync(t, f)
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("changed price accepted old checkout", e)
	}
	for _, req := range []c.CreateOrder{
		{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 2}}},
		{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}, Meta: map[string]json.RawMessage{"app_subscription": json.RawMessage(`{}`)}},
	} {
		if _, e = f.r.CreateOrder(f.ctx, req, ""); e == nil {
			t.Fatal("ambiguous or forged recurrence accepted")
		}
	}
	query(t, f.db, `UPDATE owned_app_secrets SET revoked_at=now() WHERE id=$1`, f.secretID)
	if _, e = f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}}, ""); e == nil {
		t.Fatal("revoked secret created checkout")
	}
}

func TestPGRecurringProductOppositeWalletPayments(t *testing.T) {
	a, b := newRecurringProductFixture(t), newRecurringProductFixture(t)
	for _, f := range []commerceFixture{a, b} {
		query(t, f.db, `UPDATE commerce_v1.merchants SET wallet_id=$2 WHERE app_id=$1`, f.app, f.wallet.ID)
		recurringSync(t, f)
	}
	query(t, a.db, `INSERT INTO owned_app_consents(account_id,app_id,scopes,expires_at,created_at,updated_at) VALUES($1,$2,'["accounts.profile.board"]',now()+interval '1 hour',now(),now()),($3,$4,'["accounts.profile.board"]',now()+interval '1 hour',now(),now())`, a.actor, b.app, b.actor, a.app)
	oa, ob := recurringOrder(t, a, "opposite-a"), recurringOrder(t, b, "opposite-b")
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; _, e := a.r.PayOrder(a.ctx, a.actor, ob.ID, &a.wallet.ID); done <- e }()
	go func() { <-start; _, e := b.r.PayOrder(b.ctx, b.actor, oa.ID, &b.wallet.ID); done <- e }()
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatal("opposite app purchases deadlocked or failed", e)
		}
	}
	walletAssertBalance(t, walletpg.New(a.db), a.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	walletAssertBalance(t, walletpg.New(b.db), b.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
}

func TestPGRecurringProductHostPINCatalogAndCancel(t *testing.T) {
	f := newRecurringProductFixture(t)
	host := backendApp(t, f.db, f.config)
	var developer, project string
	if e := f.db.QueryRow(`SELECT pub.name,a.project_id::text FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id WHERE a.id=$1`, f.app).Scan(&developer, &project); e != nil {
		t.Fatal(e)
	}
	path := "/develop/private/apps/" + f.app + "/products/" + f.product + "?dev=" + developer + "&proj=" + project
	backendCall(t, host, "PATCH", path, f.token, `{"recurrence":"weekly","price":3.1239}`, 200)
	d := recurringDefinition(t, f)
	if d.CycleDurationDays != 7 || d.BasePrice.String() != "3.123" {
		t.Fatal("host catalog hook missing", d)
	}
	o := backendDecode[c.Order](t, backendJSONCall(t, host, "POST", "/wallet/orders", "", c.CreateOrder{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}}, 200))
	payPath := "/wallet/orders/" + o.ID + "/pay"
	body := map[string]any{"payer_wallet_id": f.wallet.ID}
	backendJSONCall(t, host, "POST", payPath, f.token, body, 503)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at) VALUES($1::uuid,false,now())`, f.actor)
	paid := backendDecode[c.Order](t, backendJSONCall(t, host, "POST", payPath, f.token, body, 200))
	if paid.Status != c.Finished {
		t.Fatal(paid)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.actor); n != 1 {
		t.Fatal("PIN policy proof not consumed atomically", n)
	}
	s := backendDecode[c.Subscription](t, backendCall(t, host, "GET", "/wallet/subscriptions/"+d.Identifier, f.token, "", 200))
	backendCall(t, host, "POST", "/wallet/subscriptions/"+s.ID+"/cancel", f.token, `{}`, 200)
	backendCall(t, host, "POST", "/wallet/subscriptions/"+d.Identifier+"/checkout/paddle", f.token, `{}`, 503)
	backendCall(t, host, "DELETE", path, f.token, "", 204)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.definitions WHERE identifier=$1 AND enabled`, d.Identifier); n != 0 {
		t.Fatal("deleted product still sells", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.877", "0.000", "96.877")
}

func TestPGRecurringProductRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	if phase == "after" {
		db := database(t)
		var saved struct{ OrderID string }
		loadRestartFixture(t, "recurring-product-restart.json", &saved)
		var raw []byte
		if e := db.QueryRow(`SELECT document FROM commerce_v1.orders WHERE id=$1::uuid AND document->>'remarks'='Synthetic recurring restart'`, saved.OrderID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var o c.Order
		if e := json.Unmarshal(raw, &o); e != nil {
			t.Fatal(e)
		}
		if o.Status != c.Finished || o.TransactionID == nil || o.ProductIdentifier == nil {
			t.Fatal(o)
		}
		var s c.Subscription
		if e := db.QueryRow(`SELECT s.document FROM commerce_v1.orders o JOIN commerce_v1.subscriptions s ON s.id=o.subscription_id WHERE o.id=$1`, o.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal(raw, &s); e != nil {
			t.Fatal(e)
		}
		if s.Status != c.SubscriptionActive || s.ProductTerms == "" || s.ConsentVersion < 1 || s.RenewalAt != nil || s.IsAutoRenewal {
			t.Fatal(s)
		}
		walletAssertBalance(t, walletpg.New(db), *o.PayerWalletID, "synthetic-commerce", "97.877", "0.000", "97.877")
		if n := commerceCount(t, db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 1 {
			t.Fatal(n)
		}
		if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.subscription.activated'`, s.ID); n != 1 {
			t.Fatal(n)
		}
		return
	}
	f := newRecurringProductFixture(t)
	o, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Remarks: stringPointer("Synthetic recurring restart"), Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}}, "restart")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	// A fresh repository and connection pool use persisted records exclusively.
	r, ctx := commercePool(t, f)
	d := recurringDefinition(t, f)
	if s, e := r.Subscription(ctx, f.actor, d.Identifier); e != nil || s.ProductTerms == "" {
		t.Fatal(s, e)
	}
	if _, e = r.PayOrder(context.Background(), f.actor, o.ID, &f.wallet.ID); e == nil {
		t.Fatal("missing bound authority accepted")
	}
	wrong := commercepg.New(database(t), f.guard, commercepg.AppAdapters(apppg.New(f.db)))
	if _, e = wrong.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e == nil {
		t.Fatal("cross-pool proof accepted")
	}
	saveRestartFixture(t, "recurring-product-restart.json", struct{ OrderID string }{o.ID})
}
