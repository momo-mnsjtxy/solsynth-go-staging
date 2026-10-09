// SPDX-License-Identifier: AGPL-3.0-only
package surveys

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type subscriptionHTTPRepo struct {
	Repository
	called string
}

func (r *subscriptionHTTPRepo) Subscribe(context.Context, string, string) (Subscription, error) {
	r.called = "subscribe"
	return Subscription{}, nil
}
func (r *subscriptionHTTPRepo) Subscription(context.Context, string, string) (Subscription, error) {
	r.called = "subscription"
	return Subscription{}, ErrSubscriptionNotFound
}
func (r *subscriptionHTTPRepo) Unsubscribe(context.Context, string, string) error {
	r.called = "unsubscribe"
	return nil
}

type subscriptionHTTPAuth struct{ permission string }

func (a *subscriptionHTTPAuth) Authenticate(*http.Request) (string, error) {
	return "10000000-0000-4000-8000-000000000001", nil
}
func (a *subscriptionHTTPAuth) Authorize(_ *http.Request, p string) error {
	a.permission = p
	return nil
}
func TestSubscriptionAuthoredHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		method, action, body, permission string
		status                           int
	}{
		{"POST", "subscribe", "", "surveys.subscribe", 200},
		{"POST", "unsubscribe", "", "surveys.subscribe", 204},
		{"GET", "subscription", "", "", 404},
		{"POST", "subscribe", "{}", "surveys.subscribe", 400},
		{"POST", "unsubscribe?account_id=foreign", "", "surveys.subscribe", 400},
		{"GET", "subscription?account_id=foreign", "", "", 400},
	} {
		repo := &subscriptionHTTPRepo{}
		auth := &subscriptionHTTPAuth{}
		req := httptest.NewRequest(tc.method, "/api/surveys/10000000-0000-4000-8000-000000000002/"+tc.action, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		NewHandler(repo, auth).ServeHTTP(w, req)
		if w.Code != tc.status || auth.permission != tc.permission {
			t.Fatal(tc, w.Code, auth.permission, w.Body.String())
		}
		if tc.status == 400 && repo.called != "" {
			t.Fatal("invalid request reached repository")
		}
		if tc.status == 204 && w.Body.Len() != 0 {
			t.Fatal("unsubscribe body")
		}
	}
}
