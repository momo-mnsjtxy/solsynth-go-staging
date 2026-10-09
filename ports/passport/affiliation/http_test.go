// SPDX-License-Identifier: AGPL-3.0-only
package affiliation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

type fixtureRepo struct {
	calls int
	last  Command
}

func (r *fixtureRepo) Execute(_ context.Context, _ string, c Command) (Result, error) {
	r.calls++
	r.last = c
	return Result{Value: map[string]bool{"ok": true}}, nil
}
func (r *fixtureRepo) BindAuthority(c context.Context, _ authority.Principal) context.Context {
	return c
}

type fixtureAuthority struct{}

func (fixtureAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") == "Bearer synthetic" {
		return authority.Principal{Account: authority.Account{ID: "00000000-0000-4000-8000-000000000001"}}, nil
	}
	return authority.Principal{}, authority.ErrInvalid
}
func (fixtureAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}
func TestAffiliationStrictShapesAndAnonymousBoundary(t *testing.T) {
	for _, body := range []string{`null`, `{"spell":"A","spell":"B"}`, `{"spell":"A","Spell":"B"}`, `{"spell":"A","type":1}`, `{"max_usages":1.1}`, `{"max_usages":9223372036854775808}`, `{"skip_tests":"true"}`, `{} {}`, `{"account_id":"synthetic"}`} {
		repo := &fixtureRepo{}
		h := (&Service{repo, fixtureAuthority{}}).Handler()
		r := httptest.NewRequest("POST", "/api/affiliations", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || repo.calls != 0 {
			t.Fatal(body, w.Code, repo.calls)
		}
	}
	for _, tc := range []struct {
		token   string
		present bool
		status  int
	}{{"", false, 200}, {"", true, 401}, {"bad", true, 401}, {"Bearer synthetic", true, 200}} {
		repo := &fixtureRepo{}
		r := httptest.NewRequest("POST", "/api/affiliations/CODE/results", strings.NewReader(`{"resource_identifier":"synthetic:page"}`))
		r.Header.Set("Content-Type", "application/json")
		if tc.present {
			r.Header["Authorization"] = []string{tc.token}
		}
		w := httptest.NewRecorder()
		(&Service{repo, fixtureAuthority{}}).Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
	}
}
func TestAffiliationRootAndInputBoundaries(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	p := authority.Principal{Account: authority.Account{ID: id}, Session: authority.Session{ID: id, AccountID: id}}
	if e := Root(p); e != nil {
		t.Fatal(e)
	}
	p.Session.ParentID = id
	if !errors.Is(Root(p), authority.ErrForbidden) {
		t.Fatal("delegated root")
	}
	for _, s := range []string{"", " bad", "a/b", "a\\b", "a\n", strings.Repeat("a", 1025)} {
		if ValidWord(s) {
			t.Fatal(s)
		}
	}
	if !ValidWord("AbC-123") || ValidResource("") {
		t.Fatal("input boundary")
	}
}

func TestPaidInviteHTTPRequiresExactIdempotencyAndCanonicalActor(t *testing.T) {
	repo := &fixtureRepo{}
	h := (&Service{repo, fixtureAuthority{}}).Handler()
	body := `{"payer_wallet_id":"00000000-0000-4000-8000-000000000002","max_usages":2,"expires_at":"2030-01-02T03:04:05Z","remark":"registration invite"}`
	for _, keys := range [][]string{nil, {"one", "two"}} {
		r := httptest.NewRequest("POST", "/api/affiliations/purchase", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic")
		for _, key := range keys {
			r.Header.Add("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || repo.calls != 0 {
			t.Fatal(keys, w.Code, repo.calls)
		}
	}
	r := httptest.NewRequest("POST", "/api/affiliations/purchase", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("Idempotency-Key", "purchase-one")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || repo.calls != 1 || repo.last.Operation != "purchase" || repo.last.Purchase == nil || repo.last.Purchase.Key != "purchase-one" || repo.last.Purchase.MaxUsages != 2 {
		t.Fatal(w.Code, w.Body.String(), repo.calls, repo.last)
	}
}
