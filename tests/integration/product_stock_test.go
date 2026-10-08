// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync"
	"testing"
	"time"
)

func stockConfigure(t *testing.T, f commerceFixture, mode string, quantity int64) {
	t.Helper()
	raw, _ := json.Marshal(apps.ProductState{IsEnabled: true, StockMode: mode, StockQuantity: &quantity})
	query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{state}',$2::jsonb) WHERE id=$1::uuid`, f.product, raw)
}
func stockOrder(f commerceFixture, quantity int, key string) (c.Order, error) {
	return f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: quantity}}}, key)
}

func TestPGProductStockConcurrentReservationConsumeAndRelease(t *testing.T) {
	f := newCommerceFixture(t)
	stockConfigure(t, f, "daily", 2)
	var wg sync.WaitGroup
	orders := make(chan c.Order, 10)
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := stockOrder(f, 1, "")
			if e == nil {
				orders <- o
			} else {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(orders)
	close(errs)
	for e := range errs {
		if !errors.Is(e, c.ErrStock) {
			t.Fatal(e)
		}
	}
	got := []c.Order{}
	for o := range orders {
		got = append(got, o)
	}
	if len(got) != 2 {
		t.Fatal("reservation oversold", len(got))
	}
	if _, e := f.r.PayOrder(f.ctx, f.actor, got[0].ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
		t.Fatal("paid order did not consume", e)
	}
	if _, e := f.r.UpdateOrder(f.ctx, got[0].ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e != nil {
		t.Fatal(e)
	}
	if _, e := stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
		t.Fatal("finished order released inventory", e)
	}
	if _, e := f.r.UpdateOrder(f.ctx, got[1].ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	replacement, e := stockOrder(f, 1, "replacement")
	if e != nil {
		t.Fatal("cancellation did not release", e)
	}
	again, e := stockOrder(f, 1, "replacement")
	if e != nil || again.ID != replacement.ID {
		t.Fatal("idempotency consumed twice", again, e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, replacement.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, replacement.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	if _, e = stockOrder(f, 1, "after-refund"); e != nil {
		t.Fatal("refunded stock not released", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "97.877", "0.000", "97.877")
}

func TestPGProductStockUTCWindowsExpiryAndRollback(t *testing.T) {
	for _, mode := range []string{"daily", "weekly", "monthly", "yearly"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommerceFixture(t)
			stockConfigure(t, f, mode, 1)
			start := time.Date(2030, 12, 31, 23, 59, 59, 0, time.UTC)
			if mode == "weekly" {
				start = time.Date(2030, 12, 29, 23, 59, 59, 0, time.UTC)
			} // Sunday.
			f.clock.Store(start.Unix())
			o, e := stockOrder(f, 1, "old-window")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
				t.Fatal("old window oversold", e)
			}
			f.clock.Store(start.Add(time.Second).Unix())
			current, e := stockOrder(f, 1, "current-window")
			if e != nil {
				t.Fatal("new UTC window unavailable", mode, e)
			}
			if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
				t.Fatal(e)
			}
			if _, e = stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
				t.Fatal("old window refund enlarged new stock", e)
			}
			f.clock.Store(current.ExpiredAt.Unix())
			if _, e = stockOrder(f, 1, "expired-reservation"); e != nil {
				t.Fatal("expired unpaid reservation retained stock", e)
			}
		})
	}
	f := newCommerceFixture(t)
	stockConfigure(t, f, "manual", 1)
	query(t, f.db, `ALTER TABLE commerce_v1.outbox ADD CONSTRAINT synthetic_stock_failure CHECK(event_type<>'commerce.order.created' OR payload->>'app_identifier'<>'developer.app:`+f.app+`') NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE commerce_v1.outbox DROP CONSTRAINT IF EXISTS synthetic_stock_failure`)
	})
	if _, e := stockOrder(f, 1, ""); e == nil {
		t.Fatal("outbox failure not injected")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.order_items WHERE product_id=$1`, f.product); n != 0 {
		t.Fatal("rollback leaked reservation", n)
	}
	query(t, f.db, `ALTER TABLE commerce_v1.outbox DROP CONSTRAINT synthetic_stock_failure`)
	if _, e := stockOrder(f, 1, ""); e != nil {
		t.Fatal("rollback consumed capacity", e)
	}
}

func TestPGProductStockManualRestockThroughHost(t *testing.T) {
	f := newCommerceFixture(t)
	f.r.WithClock(time.Now)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "subscriptions.*"}, {Pattern: "wallets.*"}, {Pattern: "developers.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	old, e := stockOrder(f, 20, "prior-manual-window")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, old.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, old.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e != nil {
		t.Fatal(e)
	}
	host := backendApp(t, f.db, f.config)
	var developer, project string
	if e = f.db.QueryRow(`SELECT pub.name,a.project_id::text FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id WHERE a.id=$1`, f.app).Scan(&developer, &project); e != nil {
		t.Fatal(e)
	}
	path := "/develop/private/apps/" + f.app + "/products/" + f.product + "?dev=" + developer + "&proj=" + project
	product := backendDecode[apps.Product](t, backendCall(t, host, "PATCH", path, f.token, `{"state":{"stock_quantity":3}}`, 200))
	if product.State.LastRestockedAt == nil || product.State.LastRestockedQuantity == nil || *product.State.LastRestockedQuantity != 3 {
		t.Fatal("restock metadata absent", product)
	}
	public := backendDecode[apps.Product](t, backendCall(t, host, "GET", "/develop/apps/commerce-"+f.app+"/products/synthetic-widget", "", "", 200))
	if public.State.LastRestockedAt == nil || !public.State.LastRestockedAt.Equal(*product.State.LastRestockedAt) {
		t.Fatal(public)
	}
	reserved, e := stockOrder(f, 3, "first-restock")
	if e != nil {
		t.Fatal("restock retained old consumption", e)
	}
	if _, e = stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
		t.Fatal(e)
	}
	backendCall(t, host, "PATCH", path, f.token, `{"state":{"stock_quantity":1}}`, 200)
	if _, e = stockOrder(f, 1, "second-restock"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, reserved.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	if _, e = stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
		t.Fatal("cancel from previous restock released new capacity", e)
	}
	backendCall(t, host, "PATCH", path, f.token, `{"state":{"stock_mode":"hourly"}}`, 501)
	backendCall(t, host, "PATCH", path, f.token, `{"state":{"stock_quantity":-1}}`, 400)
}

func TestPGProductStockRecurringReservationRenewalAndCancel(t *testing.T) {
	f := newRecurringProductFixture(t)
	stockConfigure(t, f, "manual", 2)
	recurringSync(t, f)
	d := recurringDefinition(t, f)
	days := 60
	yes := true
	s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier, CycleDurationDays: &days, IsAutoRenewal: &yes}, "finite-cycle", false)
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.Identifier)
	if e != nil || len(o.Items) != 1 || o.Items[0].Quantity != 2 || o.Amount.String() != "4.247" {
		t.Fatal(o, e)
	}
	again, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.Identifier)
	if e != nil || again.ID != o.ID {
		t.Fatal("reused order reserved twice", e)
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
		t.Fatal(e)
	}
	s, e = f.r.Subscription(f.ctx, f.actor, s.Identifier)
	if e != nil || s.Status != c.SubscriptionExpired {
		t.Fatal("out-of-stock renewal stayed active", s, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders WHERE subscription_id=$1`, s.ID); n != 1 {
		t.Fatal("failed renewal retained tentative order", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "0.000", "95.753")
	other := newRecurringProductFixture(t)
	stockConfigure(t, other, "manual", 1)
	recurringSync(t, other)
	def := recurringDefinition(t, other)
	pending, e := other.r.CreateSubscription(other.ctx, other.actor, c.CreateSubscription{Identifier: def.Identifier}, "pending-stock", false)
	if e != nil {
		t.Fatal(e)
	}
	order, e := other.r.SubscriptionOrder(other.ctx, other.actor, pending.Identifier)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.r.CancelSubscription(other.ctx, other.actor, pending.ID); e != nil {
		t.Fatal(e)
	}
	cancelled, e := other.r.GetOrder(other.ctx, other.actor, order.ID, false)
	if e != nil || cancelled.Status != c.Cancelled {
		t.Fatal("cancel retained reservation", cancelled, e)
	}
	if _, e = other.r.CreateSubscription(other.ctx, other.actor, c.CreateSubscription{Identifier: def.Identifier}, "replacement-stock", false); e != nil {
		t.Fatal(e)
	}
	if _, e = other.r.SubscriptionOrder(other.ctx, other.actor, def.Identifier); e != nil {
		t.Fatal("cancelled subscription blocked new stock", e)
	}
	walletAssertBalance(t, walletpg.New(other.db), other.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
}

func TestPGProductStockConcurrentSubscriptionBuyers(t *testing.T) {
	a, b := newRecurringProductFixture(t), newCommerceFixture(t)
	stockConfigure(t, a, "monthly", 1)
	recurringSync(t, a)
	d := recurringDefinition(t, a)
	query(t, a.db, `INSERT INTO owned_app_consents(account_id,app_id,scopes,expires_at,created_at,updated_at) VALUES($1,$2,'["accounts.profile.board"]',now()+interval '1 hour',now(),now())`, b.actor, a.app)
	for _, f := range []commerceFixture{a, b} {
		if _, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier}, "competing-subscription", false); e != nil {
			t.Fatal(e)
		}
	}
	type result struct {
		fixture commerceFixture
		order   c.Order
		err     error
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	for _, f := range []commerceFixture{a, b} {
		go func() { <-start; o, e := f.r.SubscriptionOrder(f.ctx, f.actor, d.Identifier); done <- result{f, o, e} }()
	}
	close(start)
	var winner, loser result
	wins := 0
	for i := 0; i < 2; i++ {
		r := <-done
		if r.err == nil {
			winner = r
			wins++
		} else if errors.Is(r.err, c.ErrStock) {
			loser = r
		} else {
			t.Fatal("subscription admission lock failure", r.err)
		}
	}
	if wins != 1 {
		t.Fatal("shared stock oversold", wins)
	}
	if _, e := winner.fixture.r.CancelSubscription(winner.fixture.ctx, winner.fixture.actor, d.Identifier); e != nil {
		t.Fatal(e)
	}
	if _, e := loser.fixture.r.SubscriptionOrder(loser.fixture.ctx, loser.fixture.actor, d.Identifier); e != nil {
		t.Fatal(e)
	}
}

func TestPGProductStockRestockAndWindowWaits(t *testing.T) {
	for _, mode := range []string{"manual", "daily"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommerceFixture(t)
			before := time.Date(2031, 1, 1, 23, 59, 59, 0, time.UTC)
			after := before.Add(time.Second)
			f.clock.Store(before.Unix())
			stockConfigure(t, f, mode, 1)
			if mode == "daily" {
				o, e := stockOrder(f, 1, "prior-boundary")
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
					t.Fatal(e)
				}
			} else {
				stockConfigure(t, f, mode, 0)
			}
			block, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_app_products", f.product)
			type result struct {
				o c.Order
				e error
			}
			done := make(chan result, 1)
			go func() { o, e := stockOrder(f, 1, "waiting-reservation"); done <- result{o, e} }()
			waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_app_products")
			if mode == "manual" {
				quantity := int64(1)
				state, _ := json.Marshal(apps.ProductState{IsEnabled: true, StockMode: "manual", StockQuantity: &quantity, LastRestockedAt: &after, LastRestockedQuantity: &quantity})
				if _, e := block.Exec(`UPDATE owned_app_products SET document=jsonb_set(document,'{state}',$2::jsonb) WHERE id=$1`, f.product, state); e != nil {
					t.Fatal(e)
				}
			}
			f.clock.Store(after.Unix())
			if e := block.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if got.e != nil || !got.o.CreatedAt.Equal(after) {
				t.Fatal("admission retained pre-wait inventory clock", mode, got.o, got.e)
			}
			if _, e := stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
				t.Fatal("stale window minted extra availability", e)
			}
		})
	}
}

func TestPGProductStockCancellationPaymentRace(t *testing.T) {
	f := newCommerceFixture(t)
	stockConfigure(t, f, "manual", 1)
	for i := 0; i < 5; i++ {
		o, e := stockOrder(f, 1, "")
		if e != nil {
			t.Fatal(e)
		}
		start := make(chan struct{})
		paid := make(chan error, 1)
		cancelled := make(chan error, 1)
		go func() { <-start; _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); paid <- e }()
		go func() {
			<-start
			_, e := f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled})
			cancelled <- e
		}()
		close(start)
		if e = <-cancelled; e != nil {
			t.Fatal(e)
		}
		if e = <-paid; e != nil && !errors.Is(e, c.ErrConflict) {
			t.Fatal(e)
		}
		walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.order.cancelled'`, o.ID); n != 1 {
			t.Fatal("duplicate cancellation", n)
		}
	}
	if _, e := stockOrder(f, 1, "final-reservation"); e != nil {
		t.Fatal(e)
	}
	if _, e := stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
		t.Fatal("race minted capacity", e)
	}
}

func TestPGProductStockRejectsMismatchedCycleEconomics(t *testing.T) {
	f := newRecurringProductFixture(t)
	stockConfigure(t, f, "monthly", 10)
	recurringSync(t, f)
	d := recurringDefinition(t, f)
	s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier}, "captured-economics", false)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET cycle_days=60 WHERE id=$1`, s.ID)
	if _, e = f.r.SubscriptionOrder(f.ctx, f.actor, s.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("duration mutation changed reservation units", e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET cycle_days=30,document=jsonb_set(document,'{final_price}','9') WHERE id=$1`, s.ID)
	if _, e = f.r.SubscriptionOrder(f.ctx, f.actor, s.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("price mutation changed captured purchase", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.order_items WHERE product_id=$1`, f.product); n != 0 {
		t.Fatal("invalid economics reserved inventory", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
}

func TestPGProductStockRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	if phase == "after" {
		db := database(t)
		var saved struct{ ReservedOrderID, PaidOrderID string }
		loadRestartFixture(t, "product-stock-restart.json", &saved)
		var paidStatus int
		if e := db.QueryRow(`SELECT status FROM commerce_v1.orders WHERE id=$1::uuid`, saved.PaidOrderID).Scan(&paidStatus); e != nil || paidStatus != int(c.Finished) {
			t.Fatal("pre-restart paid order missing", paidStatus, e)
		}
		var raw []byte
		if e := db.QueryRow(`SELECT document FROM commerce_v1.orders WHERE id=$1::uuid AND document->>'remarks'='Synthetic stock restart reserved'`, saved.ReservedOrderID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var o c.Order
		if e := json.Unmarshal(raw, &o); e != nil {
			t.Fatal(e)
		}
		app := strings.TrimPrefix(o.AppIdentifier, "developer.app:")
		var secretID string
		if e := db.QueryRow(`SELECT id::text FROM owned_app_secrets WHERE app_id=$1`, app).Scan(&secretID); e != nil {
			t.Fatal(e)
		}
		credentials := c.Credentials{ClientID: app, ClientSecret: "oa1." + secretID + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
		r := commercepg.New(db, nil, commercepg.AppAdapters(apppg.New(db)))
		req := c.CreateOrder{Credentials: credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}}
		if _, e := r.CreateOrder(t.Context(), req, ""); !errors.Is(e, c.ErrStock) {
			t.Fatal("restart forgot consumed/reserved stock", e)
		}
		if _, e := r.UpdateOrder(t.Context(), o.ID, c.UpdateStatus{Credentials: credentials, Status: c.Cancelled}); e != nil {
			t.Fatal(e)
		}
		if _, e := r.CreateOrder(t.Context(), req, "after-restart-release"); e != nil {
			t.Fatal("restart cancellation failed to release", e)
		}
		return
	}
	f := newCommerceFixture(t)
	stockConfigure(t, f, "manual", 2)
	var saved struct{ ReservedOrderID, PaidOrderID string }
	for _, suffix := range []string{"paid", "reserved"} {
		o, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Remarks: stringPointer("Synthetic stock restart " + suffix), Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}}, "restart-"+suffix)
		if e != nil {
			t.Fatal(e)
		}
		if suffix == "reserved" {
			saved.ReservedOrderID = o.ID
		}
		if suffix == "paid" {
			saved.PaidOrderID = o.ID
			if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e := stockOrder(f, 1, ""); !errors.Is(e, c.ErrStock) {
		t.Fatal(e)
	}
	saveRestartFixture(t, "product-stock-restart.json", saved)
}
