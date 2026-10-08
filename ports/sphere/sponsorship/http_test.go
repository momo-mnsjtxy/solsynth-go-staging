// SPDX-License-Identifier: AGPL-3.0-only
package sponsorship

import (
	"context"
	"net/http"
	"net/http/httptest"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
)

type fake struct {
	Repository
	calls int
}

func (f *fake) CreateOrder(_ context.Context, actor, id, key string, q Request) (OrderResponse, error) {
	f.calls++
	if actor != "synthetic-actor" || id != "synthetic-post" || key != "one" {
		return OrderResponse{}, ErrInvalid
	}
	return OrderResponse{OrderID: "synthetic-order", Amount: q.Amount}, nil
}
func (f *fake) Total(context.Context, string, string) (wallet.Amount, error) { return Minimum(), nil }
func TestSponsorHTTPStrictMoneyAndRouting(t *testing.T) {
	f := &fake{}
	h := NewHandler(f, func(*http.Request, bool) (string, error) { return "synthetic-actor", nil })
	for _, body := range []string{`{"amount":5.125}`, `{"amount":5,"amount":6}`, `{"amount":"5"}`, `{"amount":5,"post_id":"foreign"}`, `{"amount":null}`, `{"amount":5} {}`} {
		r := httptest.NewRequest("POST", "/api/posts/synthetic-post/sponsor", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "one")
		r.Header.Set("X-Account-Id", "forged")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 400
		if body == `{"amount":5.125}` {
			want = 200
		}
		if w.Code != want {
			t.Fatal(body, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "forged") {
			t.Fatal("header identity leaked")
		}
	}
	if f.calls != 1 {
		t.Fatal(f.calls)
	}
	for _, p := range []string{"/api/posts/x/sponsor", "/sphere/posts/x/sponsor/history", "/api/posts/sponsor/current", "/api/publishers/x/ads", "/api/ads/x"} {
		if !Handles(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/api/posts/x", "/api/publishers/x", "/api/ads/a%2Fb"} {
		if Handles(p) {
			t.Fatal(p)
		}
	}
}
func TestSponsorHTTPUnavailableAndNoStore(t *testing.T) {
	w := httptest.NewRecorder()
	NewHandler(nil, nil).ServeHTTP(w, httptest.NewRequest("GET", "/api/posts/sponsor/current", nil))
	if w.Code != 503 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
}
