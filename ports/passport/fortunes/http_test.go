// SPDX-License-Identifier: AGPL-3.0-only
package fortunes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"testing"
)

type authFixture struct{}

func (authFixture) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer synthetic" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: "10000000-0000-4000-8000-000000000001"}}, nil
}
func (authFixture) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}

type repoFixture struct {
	calls           int
	actor, language string
	random          bool
}

func (r *repoFixture) Catalogue(_ context.Context, actor, language string, offset, take int, random bool) ([]Saying, int64, error) {
	r.calls++
	r.actor = actor
	r.language = language
	r.random = random
	return []Saying{{"Owned synthetic saying", "Test fixture", "en"}}, 1, nil
}
func TestCatalogueHTTPAuthorityAndLanguageShape(t *testing.T) {
	repo := &repoFixture{}
	s := &Service{Repository: repo, Authority: authFixture{}}
	h := s.Handler()
	call := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("/api/fortune", ""); w.Code != 401 || repo.calls != 0 {
		t.Fatal(w.Code)
	}
	w := call("/api/fortune/random?language=EN", "Bearer synthetic")
	if w.Code != 200 || !repo.random || repo.language != "EN" || repo.actor == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, repo)
	}
	s.AllowAnonymousCatalogue = true
	repo.calls = 0
	if w = call("/api/fortune", "Bearer invalid"); w.Code != 401 || repo.calls != 0 {
		t.Fatal("invalid bearer downgraded", w.Code)
	}
	for _, path := range []string{"/api/fortune?language=en&language=zh", "/api/fortune?token=x", "/api/fortune/random?take=2", "/api/fortune?account_id=x"} {
		if w = call(path, "Bearer synthetic"); w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
}
