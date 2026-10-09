// SPDX-License-Identifier: AGPL-3.0-only
package history

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
	"time"
)

func TestAuthoredCreditDecay(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	tests := []struct {
		name string
		v    CreditRecord
		want float64
	}{
		{"permanent", CreditRecord{Delta: 12, CreatedAt: past}, 12},
		{"positive_half", CreditRecord{Delta: 40, CreatedAt: past, ExpiredAt: &future}, 20},
		{"negative_half", CreditRecord{Delta: -40, CreatedAt: past, ExpiredAt: &future}, -20},
		{"at_expiry", CreditRecord{Delta: 40, CreatedAt: past, ExpiredAt: &now}, 0},
		{"expired", CreditRecord{Delta: -40, CreatedAt: past, ExpiredAt: &past}, 0},
		{"future_equal", CreditRecord{Delta: 40, CreatedAt: future, ExpiredAt: &future}, 40},
		{"future_created", CreditRecord{Delta: 40, CreatedAt: now.Add(30 * time.Minute), ExpiredAt: &future}, 80},
		{"inverted", CreditRecord{Delta: 40, CreatedAt: future.Add(time.Hour), ExpiredAt: &future}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, e := EffectiveDelta(tt.v, now)
			if e != nil || v != tt.want {
				t.Fatal(v, e, tt.want)
			}
		})
	}
	far := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	start := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	value, e := EffectiveDelta(CreditRecord{Delta: 100, CreatedAt: start, ExpiredAt: &far}, now)
	if e != nil || value < 79 || value > 80 {
		t.Fatal("wide timestamp duration overflow", value, e)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, e := EffectiveDelta(CreditRecord{Delta: bad}, now); e == nil {
			t.Fatal("non-finite value accepted")
		}
	}
}

type fakeRepo struct {
	calls      int
	actor, ref string
	f          Filter
}

func (r *fakeRepo) Timeline(_ context.Context, actor, ref string, f Filter) ([]TimelineItem, int, error) {
	r.calls++
	r.actor = actor
	r.ref = ref
	r.f = f
	return []TimelineItem{}, 7, nil
}
func (r *fakeRepo) Actions(_ context.Context, actor string, f Filter) ([]ActionLog, int, error) {
	r.calls++
	r.actor = actor
	r.f = f
	return []ActionLog{}, 4, nil
}
func (r *fakeRepo) Credits(_ context.Context, actor, ref string) (float64, error) {
	r.calls++
	r.actor = actor
	r.ref = ref
	return 100, nil
}
func (r *fakeRepo) CreditHistory(_ context.Context, actor string, f Filter) ([]CreditRecord, int, error) {
	r.calls++
	r.actor = actor
	r.f = f
	return []CreditRecord{}, 3, nil
}
func (r *fakeRepo) Connections(_ context.Context, actor, provider string) ([]Connection, error) {
	r.calls++
	r.actor = actor
	r.ref = provider
	return []Connection{}, nil
}

type auth struct{}

func (auth) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer valid" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: "00000000-0000-4000-8000-000000000001"}}, nil
}
func TestHTTPHistoryRoutesAndStrictInputs(t *testing.T) {
	cases := []struct {
		path, token string
		status      int
	}{
		{"/api/accounts/alice/timeline", "", 200}, {"/api/accounts/alice/credits", "", 200},
		{"/api/accounts/me/actions", "", 401}, {"/api/accounts/me/connections", "", 401},
		{"/api/accounts/me/credits/history", "valid", 200}, {"/api/accounts/me/credits", "valid", 200},
		{"/api/accounts/me/actions?take=-2&offset=-3", "valid", 200},
		{"/api/accounts/me/actions?take=201", "valid", 400}, {"/api/accounts/me/actions?offset=100001", "valid", 400},
		{"/api/accounts/alice/timeline?take=0", "", 400}, {"/api/accounts/alice/timeline?take=1&take=2", "", 400},
		{"/api/accounts/alice/timeline?from=2026-01-01T00%3A00%3A00Z&to=2026-01-01T00%3A00%3A00Z", "", 400},
		{"/api/accounts/alice/timeline?token=secret", "", 400}, {"/api/accounts/me/connections?account_id=other", "valid", 400},
		{"/api/accounts/alice/timeline", "wrong", 401}, {"/api/accounts/alice/connections", "", 404},
	}
	for _, tt := range cases {
		t.Run(tt.path+tt.token, func(t *testing.T) {
			repo := &fakeRepo{}
			s := (&Service{Repository: repo, Authority: auth{}, Bind: func(c context.Context, _ authority.Principal) context.Context { return c }}).Handler()
			r := httptest.NewRequest("GET", tt.path, nil)
			if tt.token != "" {
				r.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tt.status != 200 && repo.calls != 0 {
				t.Fatal("failed request read repository")
			}
			if tt.status == 200 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Header())
			}
		})
	}
}
func TestHistoryHeadersHeadAndAuthIdentity(t *testing.T) {
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: auth{}, Bind: func(c context.Context, _ authority.Principal) context.Context { return c }}).Handler()
	r := httptest.NewRequest("HEAD", "/api/accounts/me/actions?take=-1&offset=-1", nil)
	r.Header.Set("Authorization", "Bearer valid")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("X-Total") != "4" || repo.f.Take != 1 || repo.f.Offset != 0 || repo.actor != "00000000-0000-4000-8000-000000000001" {
		t.Fatal(w.Code, w.Header(), repo)
	}
	for _, name := range []string{"X-Account-ID", "X-Test-Account", "X-Synthetic-Actor", "Proxy-Authorization", "X-App-Secret"} {
		r := httptest.NewRequest("GET", "/api/accounts/alice/credits", nil)
		r.Header.Set(name, "forged")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(name, w.Code)
		}
	}
	r = httptest.NewRequest("POST", "/api/accounts/me/actions", strings.NewReader(`{}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal(w.Code, w.Header())
	}
}
