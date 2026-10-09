// SPDX-License-Identifier: AGPL-3.0-only
package realmboosts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceGroundedPriceThresholdAndXP(t *testing.T) {
	for _, tc := range []struct {
		shares          int64
		currency, price string
	}{{1, "golds", "1.000"}, {1000000, "golds", "1000000.000"}, {1, "points", "1000.000"}, {1000000, "points", "1000000000.000"}} {
		a, e := Price(tc.shares, tc.currency)
		if e != nil || a.String() != tc.price {
			t.Fatal(tc, a, e)
		}
	}
	for _, n := range []int64{-1, 0, MaxShares + 1} {
		if _, e := Price(n, "golds"); e == nil {
			t.Fatal(n)
		}
	}
	if _, e := Price(1, "other"); e != ErrCurrency {
		t.Fatal(e)
	}
	for n, l := range map[int64]int{0: 0, 9: 0, 10: 1, 24: 1, 25: 2, 49: 2, 50: 3, 100000000: 3} {
		if Level(n) != l {
			t.Fatal(n)
		}
	}
	for perk, xp := range []int64{100, 150, 200, 250} {
		got, e := BoostXP(1, perk)
		if e != nil || got != xp {
			t.Fatal(perk, got, e)
		}
	}
	if _, e := BoostXP(1, 4); e == nil {
		t.Fatal("unknown perk")
	}
	xp, e := BoostXP(MaxShares, 3)
	if e != nil || xp != 250000000 {
		t.Fatal(xp, e)
	}
}
func TestNormalizationOwnedBounds(t *testing.T) {
	s, e := Normalize(nil)
	if e != nil || s != "golds" {
		t.Fatal(s, e)
	}
	for _, v := range []string{"points", " POINTS "} {
		s, e := Normalize(&v)
		if e != nil || s != "points" {
			t.Fatal(s, e)
		}
	}
	for _, v := range []string{"point", "goldſ", strings.Repeat("x", 129)} {
		if _, e := Normalize(&v); e == nil {
			t.Fatal(v)
		}
	}
}

type fakeRepo struct {
	calls   int
	request Request
}

func (f *fakeRepo) CreateOrder(_ context.Context, actor, ref, key string, q Request) (OrderResponse, error) {
	f.calls++
	f.request = q
	currency, e := Normalize(q.Currency)
	if e != nil {
		return OrderResponse{}, e
	}
	a, e := Price(q.Shares, currency)
	return OrderResponse{OrderID: "synthetic", Shares: q.Shares, Currency: currency, Amount: a}, e
}
func (f *fakeRepo) Status(context.Context, string, string) (Status, error) {
	f.calls++
	return Status{SupportedCurrencies: []string{"golds", "points"}}, nil
}
func (f *fakeRepo) Leaderboard(context.Context, string, string, int) ([]Entry, error) {
	f.calls++
	return []Entry{}, nil
}
func (f *fakeRepo) Experience(context.Context, string, string, string, int) ([]Experience, error) {
	f.calls++
	return []Experience{}, nil
}
func TestHTTPSourceRoutesAndStrictRequests(t *testing.T) {
	for _, path := range []string{"/api/realms/a/boosts", "/passport/realms/a/boosts", "/api/realms/a/boosts/leaderboard", "/api/realms/a/members/id/experience"} {
		if !Handles(path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/api/realms/a/boosts/other", "/api/realms/a%2Fb/boosts", "/api/realms//boosts", "/api/realms/a/boosts/leaderboard/"} {
		if Handles(path) {
			t.Fatal(path)
		}
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{{"POST", "/api/realms/a/boosts", `{"shares":2,"currency":"points"}`, 200}, {"POST", "/passport/realms/a/boosts", `{"shares":2,"shares":3}`, 400}, {"POST", "/api/realms/a/boosts", `{"shares":1,"amount":1}`, 400}, {"POST", "/api/realms/a/boosts", `{"shares":1.5}`, 400}, {"POST", "/api/realms/a/boosts", `null`, 400}, {"POST", "/api/realms/a/boosts", `{"shares":0}`, 400}, {"GET", "/api/realms/a/boosts/leaderboard?take=-1", "", 400}, {"GET", "/api/realms/a/boosts/leaderboard?take=2&take=3", "", 400}, {"GET", "/api/realms/a/boosts?take=2", "", 400}, {"DELETE", "/api/realms/a/boosts", "", 405}, {"GET", "/passport/realms/a/boosts/leaderboard?take=20", "", 200}} {
		f := &fakeRepo{}
		h := NewHandler(f, func(*http.Request, bool) (string, error) { return "actor", nil })
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}
func TestHTTPFailsClosed(t *testing.T) {
	for _, repo := range []Repository{nil, &fakeRepo{}} {
		h := NewHandler(repo, func(*http.Request, bool) (string, error) { return "", nil })
		r := httptest.NewRequest("POST", "/api/realms/a/boosts", strings.NewReader(`{"shares":1}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
	f := &fakeRepo{}
	h := NewHandler(f, func(*http.Request, bool) (string, error) { return "actor", nil })
	r := httptest.NewRequest("GET", "/api/realms/a/boosts", nil)
	r.Header.Set("X-Account-ID", "actor")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || f.calls != 0 {
		t.Fatal(w.Code, f.calls)
	}
}

func TestExperienceProjectionIsExplicitlyPartial(t *testing.T) {
	f := &fakeRepo{}
	h := NewHandler(f, func(*http.Request, bool) (string, error) { return "", nil })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/realms/r/members/m/experience", nil))
	if w.Code != 200 || w.Header().Get("X-Realm-Experience-Scope") != "realm.boost.purchase" || w.Header().Get("X-Realm-Experience-Complete") != "false" {
		t.Fatal(w.Code, w.Header())
	}
}
