// SPDX-License-Identifier: AGPL-3.0-only
package timeline

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

type subscriptionRecorder struct {
	Repository
	SubscriptionStateRepository
	calls           int
	actor, selector string
	at              *time.Time
	notify          bool
}

func (r *subscriptionRecorder) UpdateSubscriptionNotify(_ context.Context, actor, selector string, notify bool) (Subscription, error) {
	r.calls++
	r.actor = actor
	r.selector = selector
	r.notify = notify
	return Subscription{AccountID: actor, Notify: notify}, nil
}
func (r *subscriptionRecorder) UpdateSubscriptionReadStatus(_ context.Context, actor, selector string, at *time.Time) (SubscriptionReadStatus, error) {
	r.calls++
	r.actor = actor
	r.selector = selector
	r.at = at
	return SubscriptionReadStatus{Subscription: Subscription{AccountID: actor, LastReadAt: at}}, nil
}
func TestSubscriptionStateRouteValidationAndPermission(t *testing.T) {
	repo := &subscriptionRecorder{}
	auth := &fakeAuth{actor: actorID}
	h := NewHandler(repo, auth)
	w := call(h, "PATCH", "/api/publishers/example/subscription/me/notify", `{"notify":false}`)
	if w.Code != 200 || repo.calls != 1 || repo.notify || repo.actor != actorID || repo.selector != "example" || auth.permission != "publishers.subscriptions.manage" {
		t.Fatal(w.Code, repo, auth)
	}
	for _, body := range []string{`{"notify":null}`, `{"notify":true,"Notify":false}`, `{"notify":false,"account_id":"other"}`, `null`} {
		if w = call(h, "PATCH", "/api/publishers/example/subscription/me/notify", body); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if repo.calls != 1 {
		t.Fatal("malformed notify reached repository")
	}
	for _, body := range []string{`{"last_read_at":"invalid"}`, `{"last_read_at":null,"Last_Read_At":null}`, `{"publisher_id":"other"}`, `[]`} {
		if w = call(h, "PUT", "/api/publishers/example/subscription/read-status", body); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	w = call(h, "PUT", "/api/publishers/example/subscription/read-status", `{"last_read_at":"2026-10-01T02:03:04Z"}`)
	if w.Code != 200 || repo.calls != 2 || repo.at == nil || repo.at.UTC().Format(time.RFC3339) != "2026-10-01T02:03:04Z" {
		t.Fatal(w.Code, repo)
	}
	auth.deny = true
	if w = call(h, "PATCH", "/api/publishers/example/subscription/me/notify", `{"notify":true}`); w.Code != 403 || repo.calls != 2 {
		t.Fatal("denied operation reached store", w.Code)
	}
	auth.deny = false
	auth.actor = ""
	if w = call(h, "PUT", "/api/publishers/example/subscription/read-status", `{}`); w.Code != 401 || repo.calls != 2 {
		t.Fatal("missing actor reached store", w.Code)
	}
}
func TestSubscriptionStateRoutesFailClosedWithoutCapability(t *testing.T) {
	h := NewHandler(&recorderRepo{}, &fakeAuth{actor: actorID})
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/publishers/example/subscribers"}, {"POST", "/api/publishers/example/subscribers/" + actorID}, {"DELETE", "/api/publishers/example/subscribers/" + actorID},
		{"GET", "/api/publishers/example/subscription/read-status"}, {"PUT", "/api/publishers/example/subscription/read-status"}, {"PATCH", "/api/publishers/example/subscription/me/notify"},
		{"PUT", "/api/publishers/subscriptions/read-status"}, {"GET", "/api/publishers/subscriptions/live"},
	} {
		if !Handles(httptest.NewRequest(route.method, route.path, nil)) {
			t.Fatal("host missed route", route)
		}
		w := call(h, route.method, route.path, `{}`)
		if w.Code != 503 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(route, w.Code, w.Body.String())
		}
	}
}
func TestSubscriptionNotifyOmittedFieldUsesSourceDefault(t *testing.T) {
	repo := &subscriptionRecorder{}
	h := NewHandler(repo, &fakeAuth{actor: actorID})
	w := call(h, "PATCH", "/api/publishers/example/subscription/me/notify", `{}`)
	if w.Code != 200 || repo.calls != 1 || repo.notify {
		t.Fatal("source omission default", w.Code, repo)
	}
	w = call(h, "PATCH", "/api/publishers/example/subscription/me/notify", `{"notify":null}`)
	if w.Code != 400 || repo.calls != 1 {
		t.Fatal("null bool accepted", w.Code, repo)
	}
}
