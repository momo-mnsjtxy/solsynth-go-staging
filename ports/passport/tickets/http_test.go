// SPDX-License-Identifier: AGPL-3.0-only
package tickets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

const actor = "11111111-1111-4111-8111-111111111111"

type fakeAuthority struct{}

func (fakeAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer test" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: actor}}, nil
}
func (fakeAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}

type fakeRepository struct {
	calls   int
	command Command
	actor   string
}

func (f *fakeRepository) Execute(_ context.Context, a string, c Command) (Result, error) {
	f.calls++
	f.command = c
	f.actor = a
	return Result{Value: map[string]any{"ok": true}}, nil
}
func call(h http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if auth {
		r.Header.Set("Authorization", "Bearer test")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestTicketAndExamHTTPRoutesPreserveRequestFields(t *testing.T) {
	for _, tc := range []struct{ method, path, body, op string }{{"POST", "/api/tickets", `{"title":"Help","content":"case","type":0}`, "ticket.create"}, {"GET", "/api/tickets/me?take=2&offset=1", "", "ticket.me"}, {"POST", "/api/tests/entry/attempts", `{"categories":["one","two","three"]}`, "attempt.start"}, {"POST", "/api/tests/attempts/" + actor + "/submit", `{"answers":[]}`, "attempt.submit"}, {"POST", "/api/admin/tests/answers/" + actor + "/review", `{"is_correct":true,"awarded_points":2}`, "answer.review"}, {"GET", "/api/admin/test-questions?groupKey=entry", "", "question.list"}, {"POST", "/api/admin/tests/entry/archive?archived=true", "", "test.archive"}} {
		repo := &fakeRepository{}
		h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
		w := call(h, tc.method, tc.path, tc.body, true)
		if w.Code != 200 || repo.calls != 1 || repo.command.Operation != tc.op {
			t.Fatal(tc, w.Code, w.Body.String(), repo.command)
		}
		if tc.op == "ticket.create" && repo.command.TicketCreate.Priority != 1 {
			t.Fatal("source medium default")
		}
	}
}
func TestMalformedAndUnauthenticatedHTTPNeverReachesRepository(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"title":"a","title":"b"}`, `{"title":"a","creator_id":"other"}`, `{"title":"a"}{}`, `{"title":"a","resources":{"x":1}}`} {
		repo := &fakeRepository{}
		w := call((&Service{Repository: repo, Authority: fakeAuthority{}}).Handler(), "POST", "/api/tickets", body, true)
		if w.Code != 400 || repo.calls != 0 {
			t.Fatal(body, w.Code, repo.calls)
		}
	}
	for _, tc := range []struct {
		path, body string
		status     int
	}{{"/api/tests", "", 200}, {"/api/tests/entry", "", 401}, {"/api/tickets/me", "", 401}, {"/api/tests?token=x", "", 400}, {"/api/admin/tests", "", 401}} {
		repo := &fakeRepository{}
		w := call((&Service{Repository: repo, Authority: fakeAuthority{}}).Handler(), "GET", tc.path, tc.body, false)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
	}
}
func TestNestedDuplicateKeysAndIdentityHeadersAreRejected(t *testing.T) {
	repo := &fakeRepository{}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
	w := call(h, "POST", "/api/admin/test-question-groups", `{"key":"g","title":"Group","config":{"correct":1,"correct":2}}`, true)
	if w.Code != 400 || repo.calls != 0 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/api/tests", nil)
	r.Header.Set("X-Account-ID", actor)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || repo.calls != 0 {
		t.Fatal(w.Code)
	}
}
