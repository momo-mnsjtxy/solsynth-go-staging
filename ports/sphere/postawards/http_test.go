// SPDX-License-Identifier: AGPL-3.0-only
package postawards

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recorder struct {
	Repository
	calls   int
	request Request
}

func (f *recorder) CreateOrder(_ context.Context, actor, post, key string, q Request) (OrderResponse, error) {
	f.calls++
	f.request = q
	return OrderResponse{OrderID: "synthetic-order"}, nil
}
func TestAuthDecoderAndExistingClientSequence(t *testing.T) {
	for _, tc := range []struct {
		body, actor   string
		status, calls int
	}{{`{"amount":5.125,"message":"Synthetic"}`, "actor", 200, 1}, {`{"amount":5,"attitude":2}`, "actor", 200, 1}, {`{"amount":5,"amount":6}`, "actor", 400, 0}, {`{"amount":5,"beneficiary":"arbitrary"}`, "actor", 400, 0}, {`{"amount":5}`, "", 401, 0}} {
		repo := &recorder{}
		handler := NewHandler(repo, func(*http.Request, bool) (string, error) { return tc.actor, nil })
		request := httptest.NewRequest("POST", "/sphere/posts/11111111-1111-4111-8111-111111111111/awards", strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != tc.status || repo.calls != tc.calls {
			t.Fatal(tc, w.Code, w.Body.String(), repo.calls)
		}
		if w.Code == 200 && !strings.Contains(w.Body.String(), `"order_id":"synthetic-order"`) {
			t.Fatal(w.Body.String())
		}
		if tc.body == `{"amount":5.125,"message":"Synthetic"}` && repo.request.Attitude != Positive {
			t.Fatal("omitted source attitude changed")
		}
	}
}
func TestExactRoutesAndServices(t *testing.T) {
	for _, p := range []string{"/api/posts/x/awards", "/sphere/posts/x/awards/pending", "/api/merchants/name/wallet", "/wallet/merchants/name/settlements/settle"} {
		if !Handles(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/wallet/posts/x/awards", "/sphere/merchants/name/wallet", "/api/merchants/name/orders", "/api/posts/x/awards/pending/extra", "/api/posts/x%2fy/awards"} {
		if Handles(p) {
			t.Fatal(p)
		}
	}
}
