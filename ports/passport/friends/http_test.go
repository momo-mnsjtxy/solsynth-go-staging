// SPDX-License-Identifier: AGPL-3.0-only
package friends

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

type auth struct{}

func (auth) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer original" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: "11111111-1111-4111-8111-111111111111"}}, nil
}

type spy struct {
	calls  int
	actor  string
	filter Filter
}

func (s *spy) Overview(_ context.Context, a string, f Filter) ([]Item, int, error) {
	s.calls++
	s.actor = a
	s.filter = f
	return nil, 4, nil
}
func TestFriendsOverviewWireAndClosedInputs(t *testing.T) {
	s := &spy{}
	h := (&Service{Repository: s, Authority: auth{}, Bind: func(c context.Context, _ authority.Principal) context.Context { return c }}).Handler()
	for _, prefix := range []string{"/api", "/passport"} {
		r := httptest.NewRequest("GET", prefix+"/friends/overview?includeOffline=true&offset=1&take=2", nil)
		r.Header.Set("Authorization", "Bearer original")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != "[]\n" || w.Header().Get("X-Total") != "4" || w.Header().Get("Cache-Control") != "no-store" || !s.filter.IncludeOffline || s.filter.Offset != 1 || s.filter.Take != 2 || s.actor == "" {
			t.Fatal(w.Code, w.Body.String(), s)
		}
	}
	for _, q := range []string{"?includeOffline=", "?includeOffline=true&includeOffline=false", "?includeOffline=null", "?take=201", "?take=0", "?offset=-1", "?offset=100001", "?account_id=other", "?token=original", "?includeExpired=true", "?catalogId=private", "?take=2;offset=1", "?%FF=x"} {
		before := s.calls
		r := httptest.NewRequest("GET", "/api/friends/overview"+q, nil)
		r.Header.Set("Authorization", "Bearer original")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || s.calls != before {
			t.Fatal(q, w.Code, s.calls, before)
		}
	}
	for _, token := range []string{"", "forged"} {
		r := httptest.NewRequest("GET", "/api/friends/overview", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	for _, header := range []string{"X-Account-ID", "X-User-ID", "X-Role", "Proxy-Authorization", "X-Test-User", "X-Synthetic-User"} {
		r := httptest.NewRequest("GET", "/api/friends/overview", nil)
		r.Header.Set("Authorization", "Bearer original")
		r.Header.Set(header, "forged")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(header, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/friends/overview", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Fatal(w.Code)
	}
}
