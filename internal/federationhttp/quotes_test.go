// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"testing"
)

type quoteHTTPRepo struct {
	fixtureRepo
	revoked, actor string
}

func (r *quoteHTTPRepo) QuoteAuthorization(_ context.Context, id string) (f.QuoteAuthorization, error) {
	return f.QuoteAuthorization{ID: id, Type: "QuoteAuthorization"}, nil
}
func (r *quoteHTTPRepo) RevokeQuoteAuthorization(_ context.Context, actor, id string) error {
	r.revoked, r.actor = id, actor
	return nil
}

type quoteHTTPAuthority struct{}

func (quoteHTTPAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Method == "POST" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: "00000000-0000-4000-8000-000000000001"}}, nil
}
func (quoteHTTPAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, _ ...string) (authority.Principal, error) {
	return p, nil
}
func TestQuoteHTTPProjectionAndProtectedRevoke(t *testing.T) {
	h, _ := testHandler()
	repo := &quoteHTTPRepo{}
	h.Repository = repo
	h.Authority = quoteHTTPAuthority{}
	id := "00000000-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/quote-authorizations/" + id, 200}, {"GET", "/quote-authorizations/" + id + "?token=bad", 400}, {"GET", "/quote-authorizations/malformed", 404}, {"POST", "/quote-authorizations", 401}, {"DELETE", "/quote-authorizations/" + id, 200}, {"PATCH", "/quote-authorizations/" + id, 405}} {
		w := call(h, tc.method, tc.path)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	if repo.revoked != id || repo.actor != "00000000-0000-4000-8000-000000000001" {
		t.Fatal(repo.revoked, repo.actor)
	}
}
