// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
	"time"
)

func TestTotalKeepsExactPricesUntilFinalTruncation(t *testing.T) {
	for _, test := range []struct {
		price    string
		quantity int
		want     string
	}{{"2.1239", 2, "4.247"}, {"0.0009", 1000, "0.900"}, {"9007199254740993.123", 1, "9007199254740993.123"}, {"1e-3", 1000, "1.000"}} {
		a, e := Total([]OrderItem{{UnitPrice: json.Number(test.price), Quantity: test.quantity}})
		if e != nil || a.String() != test.want {
			t.Fatalf("%s * %d: %v %v", test.price, test.quantity, a, e)
		}
	}
	if _, e := Total([]OrderItem{{UnitPrice: json.Number("79228162514264337593543950335"), Quantity: 2}}); e == nil {
		t.Fatal("overflow")
	}
	for _, p := range []string{"NaN", "1e1000000000", "-1", "0", "1/2"} {
		if ValidPrice(json.Number(p)) {
			t.Fatal(p)
		}
	}
}
func TestItemQuantityDefaultDistinguishesExplicitZero(t *testing.T) {
	var missing, zero ItemRequest
	if e := json.Unmarshal([]byte(`{"product_identifier":"one"}`), &missing); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal([]byte(`{"product_identifier":"one","quantity":0}`), &zero); e != nil {
		t.Fatal(e)
	}
	if missing.Quantity != 1 || zero.Quantity != 0 {
		t.Fatal(missing, zero)
	}
}

type identity struct{}

func (identity) Authenticate(*http.Request) (wallet.Principal, error) {
	return wallet.Principal{AccountID: "a7000000-0000-0000-0000-000000000001", SessionID: "synthetic"}, nil
}
func (identity) Require(context.Context, wallet.Principal, string) error { return nil }

type providerStub struct {
	fail     bool
	purchase VerifiedPurchase
	calls    int
}

func (p *providerStub) Checkout(context.Context, string, string, json.RawMessage) (any, error) {
	p.calls++
	return map[string]string{"checkout_url": "https://synthetic.invalid/checkout"}, nil
}
func (p *providerStub) Restore(context.Context, string, json.RawMessage) (VerifiedPurchase, error) {
	p.calls++
	if p.fail {
		return VerifiedPurchase{}, ErrForbidden
	}
	return p.purchase, nil
}
func (p *providerStub) VerifyWebhook(context.Context, http.Header, []byte) (VerifiedPurchase, error) {
	p.calls++
	if p.fail {
		return VerifiedPurchase{}, ErrForbidden
	}
	return p.purchase, nil
}

type providerStore struct {
	Store
	applied int
}

func (s *providerStore) ApplyProvider(_ context.Context, p VerifiedPurchase) (Subscription, error) {
	s.applied++
	return Subscription{ID: "synthetic", AccountID: p.AccountID}, nil
}
func TestInjectedProviderOwnershipAndVerification(t *testing.T) {
	store := &providerStore{}
	p := &providerStub{purchase: VerifiedPurchase{Provider: "paddle", ExternalID: "synthetic", AccountID: "a7000000-0000-0000-0000-000000000001", Identifier: "plan", BegunAt: time.Now(), DurationDays: 1, Testing: true}}
	s := &Service{Store: store, Identity: identity{}, Providers: map[string]Provider{"paddle": p}}
	request := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}
	if w := request("/api/subscriptions/order/restore/apple", `{"signed_transaction_info":"synthetic"}`); w.Code != 503 || p.calls != 0 {
		t.Fatal(w.Code)
	}
	p.fail = true
	if w := request("/api/subscriptions/order/handle/paddle", `{}`); w.Code != 403 || store.applied != 0 {
		t.Fatal(w.Code, store.applied)
	}
	p.fail = false
	p.purchase.AccountID = "a7000000-0000-0000-0000-000000000002"
	if w := request("/api/subscriptions/order/restore/paddle", `{"order_id":"synthetic"}`); w.Code != 403 || store.applied != 0 {
		t.Fatal(w.Code, store.applied)
	}
	p.purchase.AccountID = "a7000000-0000-0000-0000-000000000001"
	if w := request("/api/subscriptions/order/restore/paddle", `{"order_id":"synthetic"}`); w.Code != 200 || store.applied != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("/api/subscriptions/plan/checkout/paddle", `{}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	before := p.calls
	if w := request("/api/subscriptions/order/handle/paddle", strings.Repeat("x", 65537)); w.Code != 400 || p.calls != before {
		t.Fatal("oversized webhook invoked adapter", w.Code, p.calls)
	}
}

type orderStore struct {
	Store
	calls int
}

type payoutStore struct {
	Store
	request AppPayout
	key     string
}

func (s *payoutStore) CreateAppPayout(_ context.Context, q AppPayout, key string) (wallet.Transaction, error) {
	s.request = q
	s.key = key
	return wallet.Transaction{ID: "a7000000-0000-0000-0000-000000000099"}, nil
}

func TestAppPayoutRequiresOwnedStoreAndPassesIdempotency(t *testing.T) {
	unsupported := (&Service{Store: &orderStore{}}).Handler()
	w := httptest.NewRecorder()
	unsupported.ServeHTTP(w, httptest.NewRequest("POST", "/api/orders/payouts", strings.NewReader(`{"client_id":"app","client_secret":"secret","payee_account_id":"a7000000-0000-0000-0000-000000000002","currency":"points","amount":1}`)))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	s := &payoutStore{}
	req := httptest.NewRequest("POST", "/api/orders/payouts", strings.NewReader(`{"client_id":"app","client_secret":"secret","payee_account_id":"a7000000-0000-0000-0000-000000000002","currency":"points","amount":1.25}`))
	req.Header.Set("Idempotency-Key", "payout-1")
	w = httptest.NewRecorder()
	(&Service{Store: s}).Handler().ServeHTTP(w, req)
	if w.Code != 200 || s.key != "payout-1" || s.request.Amount.String() != "1.250" {
		t.Fatal(w.Code, s.key, s.request.Amount, w.Body.String())
	}
}

func (s *orderStore) CreateOrder(context.Context, CreateOrder, string) (Order, error) {
	s.calls++
	return Order{}, ErrInvalid
}
func TestHTTPStrictBodyNoMutation(t *testing.T) {
	s := &orderStore{}
	h := (&Service{Store: s}).Handler()
	for _, payload := range []string{`{"currency":"points","unknown":true}`, `{} {}`, `{"currency":"` + strings.Repeat("x", 65537) + `"}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/orders", strings.NewReader(payload)))
		if w.Code != 400 || s.calls != 0 {
			t.Fatal(w.Code, s.calls)
		}
	}
}
