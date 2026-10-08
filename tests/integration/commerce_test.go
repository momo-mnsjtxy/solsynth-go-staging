// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type commerceFixture struct {
	atomicAuthorityFixture
	r                            *commercepg.Repository
	payee                        wallet.Wallet
	app, product, plan, secretID string
	credentials                  c.Credentials
	clock                        *atomic.Int64
}

func newCommerceFixture(t *testing.T) commerceFixture {
	t.Helper()
	f := commerceFixture{atomicAuthorityFixture: newAtomicAuthorityFixture(t), clock: &atomic.Int64{}}
	var exists bool
	if e := f.db.QueryRow(`SELECT to_regclass('commerce_v1.orders') IS NOT NULL`).Scan(&exists); e != nil || !exists {
		t.Fatal("commerce schema missing from guarded disposable harness", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "subscriptions.*"}, {Pattern: "wallets.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.clock.Store(time.Now().UTC().Unix())
	f.r = commercepg.New(f.db, f.guard, commercepg.AppAdapters(apppg.New(f.db))).WithClock(func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() })
	f.payee = walletCreate(t, walletpg.New(f.db), f.actor)
	walletFund(t, f.db, f.wallet, "synthetic-commerce", "100.000")
	f.app = authorityUUID(t)
	dev, project := authorityUUID(t), authorityUUID(t)
	f.product = authorityUUID(t)
	f.plan = "synthetic-plan-" + f.app
	f.secretID = authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_developers(id,publisher_id,created_at) VALUES($1,$2,now())`, dev, f.publisher)
	query(t, f.db, `INSERT INTO owned_dev_projects(id,developer_id,slug,name,created_at,updated_at) VALUES($1,$2,'synthetic-commerce','Synthetic commerce',now(),now())`, project, dev)
	scopes := []string{"orders.create", "orders.status.manage", "orders.metrics.read"}
	config, _ := json.Marshal(apps.OAuthConfig{AllowedScopes: scopes, RedirectURIs: []string{}, AllowedGrantTypes: []string{}})
	query(t, f.db, `INSERT INTO owned_custom_apps(id,project_id,slug,name,status,oauth_config,created_at,updated_at) VALUES($1,$2,$3,'Synthetic commerce',1,$4,now(),now())`, f.app, project, "commerce-"+f.app, config)
	// Short-lived synthetic opaque material in a disposable cluster only.
	token := "oa1." + f.secretID + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	digest := sha256.Sum256([]byte("solsynth.owned-app.v1\x00" + f.app + "\x00" + token))
	scopeJSON, _ := json.Marshal(scopes)
	query(t, f.db, `INSERT INTO owned_app_secrets(id,app_id,secret_hash,scopes,expires_at,created_at,updated_at) VALUES($1,$2,$3,$4,now()+interval '1 hour',now(),now())`, f.secretID, f.app, digest[:], scopeJSON)
	f.credentials = c.Credentials{ClientID: f.app, ClientSecret: token}
	stock := int64(20)
	p := apps.Product{ID: f.product, AppID: f.app, Identifier: "synthetic-widget", DisplayName: "Synthetic widget", Currency: "synthetic-commerce", Price: json.Number("2.1239"), Recurrence: "none", State: apps.ProductState{IsEnabled: true, StockMode: "manual", StockQuantity: &stock}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	b, _ := json.Marshal(p)
	query(t, f.db, `INSERT INTO owned_app_products(id,app_id,identifier,document,created_at,updated_at) VALUES($1,$2,$3,$4,now(),now())`, p.ID, f.app, p.Identifier, b)
	query(t, f.db, `INSERT INTO commerce_v1.merchants(app_id,wallet_id) VALUES($1,$2)`, f.app, f.payee.ID)
	d := c.Definition{Identifier: f.plan, GroupIdentifier: f.plan, DisplayName: "Synthetic plan", Currency: "synthetic-commerce", BasePrice: walletAmount(t, "3.333"), CycleDurationDays: 1, AllowInternalWalletRenewal: true, AllowGift: true, AllowedPaymentMethods: []string{"solian.wallet", "paddle"}, ProviderMappings: map[string][]string{}}
	b, _ = json.Marshal(d)
	query(t, f.db, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id) VALUES($1,$2,$3)`, f.plan, b, f.payee.ID)
	return f
}
func commerceOrder(t *testing.T, f commerceFixture, key string, items bool) c.Order {
	t.Helper()
	req := c.CreateOrder{Credentials: f.credentials, Currency: "synthetic-commerce", Amount: walletAmount(t, "10.001"), Remarks: stringPointer("Synthetic commerce test")}
	if items {
		req.Items = []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 2}}
	}
	o, e := f.r.CreateOrder(f.ctx, req, key)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func commerceCount(t *testing.T, db *sql.DB, querySQL string, args ...any) int {
	t.Helper()
	var n int
	if e := db.QueryRow(querySQL, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func commercePool(t *testing.T, f commerceFixture) (*commercepg.Repository, context.Context) {
	t.Helper()
	db := database(t)
	store := authoritypg.New(db)
	v, e := authority.NewVerifier(store, f.config)
	if e != nil {
		t.Fatal(e)
	}
	p, e := v.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(store, v)
	return commercepg.New(db, g, commercepg.AppAdapters(apppg.New(db))), g.Bind(context.Background(), p)
}
func TestPGCommerceOrderSettlementAndIsolation(t *testing.T) {
	f := newCommerceFixture(t)
	o := commerceOrder(t, f, "precision", true)
	if o.Amount.String() != "4.247" || string(o.Items[0].UnitPrice) != "2.1239" {
		t.Fatal("product price rounded before order total", o)
	}
	repeat := commerceOrder(t, f, "precision", true)
	if repeat.ID != o.ID {
		t.Fatal("create replay changed order")
	}
	if _, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Currency: "synthetic-commerce", Amount: walletAmount(t, "1")}, "precision"); !errors.Is(e, c.ErrIdempotency) {
		t.Fatal(e)
	}
	paid, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
	if e != nil || paid.Status != c.Paid {
		t.Fatal(paid, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "4.247", "95.753")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	// Ordinary transfer APIs and maintenance cannot release/refund managed holds.
	plain := walletpg.New(f.db)
	query(t, f.db, `UPDATE wallet_v1.transactions SET expires_at=now()-interval '1 second' WHERE id=$1`, *paid.TransactionID)
	for _, action := range []wallet.Transition{wallet.Confirm, wallet.Reject} {
		if _, e := plain.Transition(context.Background(), *paid.TransactionID, f.payee.Owner(), action); !errors.Is(e, wallet.ErrNotFound) {
			t.Fatal("generic transition touched order hold", e)
		}
	}
	if _, e := plain.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "4.247", "95.753")
	public, e := f.r.GetOrder(f.ctx, "", o.ID, true)
	if e != nil || public.PayerWalletID != nil || public.TransactionID != nil || len(public.Meta) != 0 {
		t.Fatal("public checkout leaks private data", public, e)
	}

	if public.App == nil || public.App.ID != f.app || public.App.Slug != "commerce-"+f.app || public.App.Name != "Synthetic commerce" || public.Developer == nil || public.Developer.PublisherID != f.publisher || public.Developer.PublisherName == nil || public.IsSystemApp {
		t.Fatal("checkout omitted canonical display identity", public)
	}
	rawPublic, e := json.Marshal(public)
	if e != nil {
		t.Fatal(e)
	}
	for _, private := range []string{"client_secret", "secret_hash", "oauth_config"} {
		if strings.Contains(string(rawPublic), private) {
			t.Fatal("checkout included private app configuration", private)
		}
	}
	other := newCommerceFixture(t)
	if _, e = other.r.GetOrder(other.ctx, other.actor, o.ID, false); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("foreign mine", e)
	}
	if _, e = other.r.PayOrder(other.ctx, other.actor, o.ID, &other.wallet.ID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("foreign replay", e)
	}
	finished, e := f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished})
	if e != nil || finished.Status != c.Finished {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("finished order refunded", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "0.000", "95.753")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "4.247", "0.000", "4.247")
	cancelled := commerceOrder(t, f, "refund", false)
	if _, e = f.r.PayOrder(f.ctx, f.actor, cancelled.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, cancelled.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, cancelled.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "0.000", "95.753")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 1 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.order.paid'`, o.ID); n != 1 {
		t.Fatal(n)
	}
}
func TestPGCommerceConcurrentPaymentStockAndRollback(t *testing.T) {
	f := newCommerceFixture(t)
	o := commerceOrder(t, f, "racing-pay", false)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		r, ctx := commercePool(t, f)
		wg.Add(1)
		go func() { defer wg.Done(); _, e := r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Error(e)
		}
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "89.999", "10.001", "89.999")
	if _, e := f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{state,stock_quantity}','3') WHERE id=$1`, f.product)
	var wins atomic.Int64
	errs = make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 1}}}, fmt.Sprintf("stock-%d", i))
			if e == nil {
				wins.Add(1)
			} else if !errors.Is(e, c.ErrStock) {
				errs <- e
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if wins.Load() != 3 {
		t.Fatal("stock oversold", wins.Load())
	}
	rollback := commerceOrder(t, f, "rollback", false)
	query(t, f.db, `CREATE FUNCTION commerce_v1.reject_synthetic_paid() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='commerce.order.paid' THEN RAISE EXCEPTION 'synthetic rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_synthetic_paid BEFORE INSERT ON commerce_v1.outbox FOR EACH ROW EXECUTE FUNCTION commerce_v1.reject_synthetic_paid()`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS reject_synthetic_paid ON commerce_v1.outbox; DROP FUNCTION IF EXISTS commerce_v1.reject_synthetic_paid()`)
	})
	if _, e := f.r.PayOrder(f.ctx, f.actor, rollback.ID, &f.wallet.ID); e == nil {
		t.Fatal("injected outbox failure committed")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, rollback.ID); n != 0 {
		t.Fatal("wallet ledger survived commerce rollback", n)
	}
}
func TestPGCommerceSubscriptionRenewCancelAndGiftRedeem(t *testing.T) {
	f := newCommerceFixture(t)
	req := c.CreateSubscription{Identifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}
	s, e := f.r.CreateSubscription(f.ctx, f.actor, req, "sub", false)
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, f.plan)
	if e != nil {
		t.Fatal(e)
	}

	checkout, e := f.r.GetOrder(f.ctx, "", o.ID, true)
	if e != nil || checkout.App == nil || checkout.App.Slug != "internal" || checkout.App.Name != "Solar Network" || !checkout.IsSystemApp || checkout.Developer != nil {
		t.Fatal("internal checkout display", checkout, e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	s, e = f.r.Subscription(f.ctx, f.actor, f.plan)
	if e != nil || s.Status != c.SubscriptionActive || s.RenewalAt == nil {
		t.Fatal(s, e)
	}
	// Advance only this fixture's durable subscription deadline, preserving the
	// global maintenance clock so unrelated fixtures are never expired by a test.
	past := time.Unix(f.clock.Load(), 0).UTC().Add(-time.Second)
	s.EndedAt = past
	s.RenewalAt = &past
	b, _ := json.Marshal(s)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2,renewal_at=$2,document=$3 WHERE id=$1`, s.ID, past, b)
	if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "93.334", "0.000", "93.334")
	if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.subscription.renewed'`, s.ID); n != 1 {
		t.Fatal("renewal replay", n)
	}
	s, e = f.r.CancelSubscription(f.ctx, f.actor, f.plan)
	if e != nil || s.RenewalAt != nil || s.IsActive || s.Status != c.SubscriptionCancelled {
		t.Fatal(s, e)
	}
	gift, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "gift")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.GiftAction(f.ctx, f.actor, gift.ID, true); !errors.Is(e, c.ErrConflict) {
		t.Fatal("unpaid gift became redeemable", e)
	}
	o, e = f.r.GiftOrder(f.ctx, f.actor, gift.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	a, bf := newCommerceFixture(t), newCommerceFixture(t)
	var wg sync.WaitGroup
	var wins atomic.Int64
	errs := make(chan error, 2)
	for _, other := range []commerceFixture{a, bf} {
		wg.Add(1)
		go func(other commerceFixture) {
			defer wg.Done()
			_, e := other.r.RedeemGift(other.ctx, other.actor, gift.GiftCode)
			if e == nil {
				wins.Add(1)
			} else if !errors.Is(e, c.ErrConflict) {
				errs <- e
			}
		}(other)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if wins.Load() != 1 {
		t.Fatal("gift redeemed more than once", wins.Load())
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.subscriptions WHERE document->'payment_details'->>'order_id'=$1`, gift.ID); n != 1 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND payload->>'gift_code'<>''`, gift.ID); n != 0 {
		t.Fatal("gift capability exposed in outbox", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "90.001", "0.000", "90.001")
}
func TestPGCommerceRestartPersistence(t *testing.T) {
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var data []byte
		if e := db.QueryRow(`SELECT document FROM commerce_v1.orders WHERE document->>'remarks'='Synthetic commerce restart'`).Scan(&data); e != nil {
			t.Fatal(e)
		}
		var o c.Order
		if e := json.Unmarshal(data, &o); e != nil {
			t.Fatal(e)
		}
		if o.Status != c.Paid || o.TransactionID == nil || o.PayerWalletID == nil {
			t.Fatal(o)
		}
		walletAssertBalance(t, walletpg.New(db), *o.PayerWalletID, "synthetic-commerce", "89.999", "10.001", "89.999")
		if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND published_at IS NULL`, o.ID); n != 2 {
			t.Fatal(n)
		}
		return
	}
	f := newCommerceFixture(t)
	o := commerceOrder(t, f, "restart", false)
	o.Remarks = stringPointer("Synthetic commerce restart")
	raw, _ := json.Marshal(o)
	query(t, f.db, `UPDATE commerce_v1.orders SET document=$2 WHERE id=$1`, o.ID, raw)
	// The same DSN through another pool is not the same transaction authority.
	wrong := commercepg.New(database(t), f.guard, commercepg.AppAdapters(apppg.New(f.db)))
	if _, e := wrong.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("cross-pool guard accepted", e)
	}
	if _, e := f.r.PayOrder(context.Background(), f.actor, o.ID, &f.wallet.ID); e == nil {
		t.Fatal("missing bound authority accepted")
	}
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	p := c.VerifiedPurchase{Provider: "paddle", ExternalID: "synthetic-" + f.app, AccountID: f.actor, Identifier: f.plan, BegunAt: time.Now().UTC().Truncate(time.Second), DurationDays: 1, Testing: true}
	s, e := f.r.ApplyProvider(f.ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.r.ApplyProvider(f.ctx, p)
	if e != nil || again.ID != s.ID {
		t.Fatal("provider replay", again, e)
	}
	p.DurationDays = 2
	if _, e = f.r.ApplyProvider(f.ctx, p); !errors.Is(e, c.ErrIdempotency) {
		t.Fatal("changed receipt accepted", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "89.999", "10.001", "89.999")
	query(t, f.db, `UPDATE owned_app_secrets SET revoked_at=now() WHERE id=$1`, f.secretID)
	if _, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Currency: "synthetic-commerce", Amount: walletAmount(t, "1")}, "revoked"); e == nil {
		t.Fatal("revoked app credential accepted")
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, nil, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet"}, "revoked-grant"); e == nil {
		t.Fatal("stale policy accepted")
	}
}

// Synthetic provider adapters are deliberately local and never call providers.
type commerceTestIdentity struct {
	actor string
	ctx   context.Context
}

func (i commerceTestIdentity) Authenticate(r *http.Request) (wallet.Principal, error) {
	*r = *r.WithContext(i.ctx)
	return wallet.Principal{AccountID: i.actor, SessionID: "synthetic"}, nil
}
func (i commerceTestIdentity) Require(context.Context, wallet.Principal, string) error { return nil }
func TestPGCommerceHTTPPaymentAndUnavailableProvider(t *testing.T) {
	f := newCommerceFixture(t)
	o := commerceOrder(t, f, "http", false)
	h := (&c.Service{Store: f.r, Identity: commerceTestIdentity{f.actor, f.ctx}, Payments: walletPIN{}}).Handler()
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("/api/orders/"+o.ID+"/pay", `{"pin_code":"wrong"}`); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("/api/orders/"+o.ID+"/pay", `{"pin_code":"synthetic-proof","payer_wallet_id":"`+f.wallet.ID+`"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("/api/subscriptions/"+f.plan+"/checkout/paddle", `{}`); w.Code != 503 {
		t.Fatal("provider success fabricated", w.Code, w.Body.String())
	}
}

func TestPGCommerceExpiryAndAuthorityOrdering(t *testing.T) {
	f := newCommerceFixture(t)
	expired := commerceOrder(t, f, "expired", false)
	expired.ExpiredAt = time.Unix(f.clock.Load(), 0).UTC().Add(-time.Minute)
	b, _ := json.Marshal(expired)
	query(t, f.db, `UPDATE commerce_v1.orders SET expires_at=$2,document=$3 WHERE id=$1`, expired.ID, expired.ExpiredAt, b)
	if _, e := f.r.PayOrder(f.ctx, f.actor, expired.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders WHERE id=$1 AND status=4`, expired.ID); n != 1 {
		t.Fatal("expiry not durable", n)
	}
	gift, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{PayerWalletID: &f.wallet.ID}}, "expiry-gift")
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.GiftOrder(f.ctx, f.actor, gift.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	gift, e = f.r.Gift(f.ctx, f.actor, gift.ID)
	if e != nil {
		t.Fatal(e)
	}
	gift.ExpiresAt = time.Unix(f.clock.Load(), 0).UTC().Add(-time.Minute)
	b, _ = json.Marshal(gift)
	query(t, f.db, `UPDATE commerce_v1.gifts SET expires_at=$2,document=$3 WHERE id=$1`, gift.ID, gift.ExpiresAt, b)
	if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	gift, e = f.r.Gift(f.ctx, f.actor, gift.ID)
	if e != nil || gift.Status != c.GiftExpired {
		t.Fatal(gift, e)
	}

	appPending := commerceOrder(t, f, "app-state-wait", false)
	var project string
	if e = f.db.QueryRow(`SELECT project_id::text FROM owned_custom_apps WHERE id=$1`, f.app).Scan(&project); e != nil {
		t.Fatal(e)
	}
	for _, change := range []struct {
		disable, restore string
		id               string
	}{
		{`UPDATE owned_custom_apps SET status=2 WHERE id=$1`, `UPDATE owned_custom_apps SET status=1 WHERE id=$1`, f.app},
		{`UPDATE owned_custom_apps SET deleted_at=now() WHERE id=$1`, `UPDATE owned_custom_apps SET deleted_at=NULL WHERE id=$1`, f.app},
		{`UPDATE owned_dev_projects SET deleted_at=now() WHERE id=$1`, `UPDATE owned_dev_projects SET deleted_at=NULL WHERE id=$1`, project},
		{`UPDATE owned_publishers SET deleted_at=now() WHERE id=$1`, `UPDATE owned_publishers SET deleted_at=NULL WHERE id=$1`, f.publisher},
	} {
		query(t, f.db, change.disable, change.id)
		if _, e = f.r.PayOrder(f.ctx, f.actor, appPending.ID, &f.wallet.ID); !errors.Is(e, c.ErrForbidden) {
			t.Fatal("unavailable app ancestry accepted new payment", e)
		}
		display, e := f.r.GetOrder(f.ctx, "", appPending.ID, true)
		if e != nil || display.App != nil || display.Developer != nil {
			t.Fatal("inactive app enrichment exposed", display, e)
		}
		query(t, f.db, change.restore, change.id)
	}
	appBlock, e := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer appBlock.Rollback()
	var appPID int
	if e = appBlock.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&appPID); e != nil {
		t.Fatal(e)
	}
	if _, e = appBlock.ExecContext(f.ctx, `UPDATE owned_custom_apps SET status=2 WHERE id=$1`, f.app); e != nil {
		t.Fatal(e)
	}
	appResult := make(chan error, 1)
	go func() { _, e := f.r.PayOrder(f.ctx, f.actor, appPending.ID, &f.wallet.ID); appResult <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, appPID, "owned_custom_apps")
	if e = appBlock.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-appResult; !errors.Is(e, c.ErrForbidden) {
		t.Fatal("app disabled during lock wait accepted payment", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, appPending.ID); n != 0 {
		t.Fatal("app rejection committed ledger", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.order.paid'`, appPending.ID); n != 0 {
		t.Fatal("app rejection committed paid event", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	query(t, f.db, `UPDATE owned_custom_apps SET status=1 WHERE id=$1`, f.app)
	pending := commerceOrder(t, f, "authority-wait", false)
	blocker, e := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var pid int
	if e = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET version=version+1 WHERE id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := f.r.PayOrder(f.ctx, f.actor, pending.ID, &f.wallet.ID); result <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e == nil {
		t.Fatal("revoked account proof crossed domain lock")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, pending.ID); n != 0 {
		t.Fatal("rejected authority committed ledger", n)
	}
}
