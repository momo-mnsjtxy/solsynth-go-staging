// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

const testAccount = "10000000-0000-4000-8000-000000000001"

type testAuth struct{}

func (testAuth) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer test" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: testAccount}}, nil
}
func (testAuth) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}

type testRepo struct {
	calls int
	c     Command
	actor string
}

func (r *testRepo) Execute(_ context.Context, a string, c Command) (Result, error) {
	r.calls++
	r.c = c
	r.actor = a
	return Result{Value: []any{}}, nil
}
func httpCall(h http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if auth {
		r.Header.Set("Authorization", "Bearer test")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestCalendarHTTPBoundariesAndRecurrenceClear(t *testing.T) {
	repo := &testRepo{}
	h := (&Service{Repository: repo, Authority: testAuth{}}).Handler()
	w := httpCall(h, "PUT", "/api/accounts/me/calendar/events/"+testAccount, `{"recurrence":null,"time_zone":"America/New_York"}`, true)
	if w.Code != 200 || repo.c.Patch == nil || !repo.c.Patch.RecurrencePresent || repo.c.Patch.Recurrence != nil {
		t.Fatal(w.Code, repo.c)
	}
	for _, body := range []string{`null`, `{"title":"a","title":"b"}`, `{"account_id":"other"}`, `{"recurrence":{"frequency":1,"unknown":true}}`, `{"meta":{"x":1,"x":2}}`} {
		repo.calls = 0
		w = httpCall(h, "POST", "/api/accounts/me/calendar/events", body, true)
		if w.Code != 400 || repo.calls != 0 {
			t.Fatal(body, w.Code, repo.calls)
		}
	}
	repo.calls = 0
	w = httpCall(h, "POST", "/api/accounts/me/calendar", "", true)
	if w.Code != 404 || repo.calls != 0 {
		t.Fatal(w.Code)
	}
	w = httpCall(h, "GET", "/api/accounts/me/calendar/events?tags=a&tags=b", "", true)
	if w.Code != 200 || len(repo.c.Query.Tags) != 2 {
		t.Fatal(w.Code, repo.c.Query)
	}
}
func TestPublicReadsDoNotAcceptInvalidCredentials(t *testing.T) {
	for _, path := range []string{"/api/notable-days", "/api/accounts/someone/calendar/events", "/api/accounts/unknown/calendar/events/" + testAccount} {
		repo := &testRepo{}
		h := (&Service{Repository: repo, Authority: testAuth{}}).Handler()
		w := httpCall(h, "GET", path, "", false)
		if w.Code != 200 || repo.actor != "" {
			t.Fatal(path, w.Code)
		}
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer invalid")
		w = httptest.NewRecorder()
		repo.calls = 0
		h.ServeHTTP(w, r)
		if w.Code != 401 || repo.calls != 0 {
			t.Fatal("bad token downgraded", w.Code)
		}
	}
}
func TestOccurrenceKeyCanBeSafelyURLEncoded(t *testing.T) {
	repo := &testRepo{}
	h := (&Service{Repository: repo, Authority: testAuth{}}).Handler()
	w := httpCall(h, "GET", "/api/accounts/me/calendar/notable-days/cn%7C2026-01-01%7Cnew%20year", "", true)
	if w.Code != 200 || repo.c.OccurrenceKey != "cn|2026-01-01|new year" {
		t.Fatal(w.Code, repo.c.OccurrenceKey)
	}
	w = httpCall(h, "GET", "/api/accounts/me/calendar/notable-days/cn%2F2026", "", true)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestCalendarHTTPMediaOmissionAndRemoval(t *testing.T) {
	repo := &testRepo{}
	h := (&Service{Repository: repo, Authority: testAuth{}}).Handler()
	for _, tc := range []struct {
		body             string
		icon, background bool
	}{{`{"title":"keep media"}`, false, false}, {`{"icon_id":null}`, true, false}, {`{"background_id":null}`, false, true}, {`{"icon_id":"10000000-0000-4000-8000-000000000002","background_id":null}`, true, true}} {
		w := httpCall(h, "PUT", "/api/accounts/me/calendar/events/"+testAccount, tc.body, true)
		if w.Code != 200 || repo.c.Patch.IconPresent != tc.icon || repo.c.Patch.BackgroundPresent != tc.background {
			t.Fatal(tc, w.Code, repo.c.Patch)
		}
	}
	for _, body := range []string{`{"icon_id":null,"icon_id":null}`, `{"background_id":{}}`, `{"icon":{"url":"https://example.invalid"}}`} {
		repo.calls = 0
		w := httpCall(h, "PUT", "/api/accounts/me/calendar/events/"+testAccount, body, true)
		if w.Code != 400 || repo.calls != 0 {
			t.Fatal(body, w.Code)
		}
	}
}
