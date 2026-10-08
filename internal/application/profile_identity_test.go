// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"net/http"
	"testing"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

type profileProofKey struct{}
type profileIdentityRepository struct {
	accounts.Repository
	viewer  string
	lookups int
}

func (r *profileIdentityRepository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return context.WithValue(ctx, profileProofKey{}, p.Account.ID)
}
func (r *profileIdentityRepository) LookupAccountForViewer(ctx context.Context, viewer, ref string) (accounts.Account, error) {
	r.viewer = viewer
	r.lookups++
	if viewer != "" && ctx.Value(profileProofKey{}) != viewer {
		return accounts.Account{}, accounts.ErrForbidden
	}
	if viewer == hostAccountB {
		return accounts.Account{}, accounts.ErrNotFound
	}
	return accounts.Account{ID: hostAccountA, Name: "public-profile"}, nil
}

func TestPublicProfileRetainsVerifiedViewerAfterHeaderRemoval(t *testing.T) {
	issuer, verifier, store := hostIdentity(t)
	a, b := hostToken(t, issuer, hostAccountA, "*"), hostToken(t, issuer, hostAccountB, "*")
	repo := &profileIdentityRepository{}
	service := (&accounts.Service{Repository: repo, Authority: proofIdentity{identity{verifier}}}).Handler()
	h := bindIdentity(verifier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("raw bearer reached account handler")
		}
		service.ServeHTTP(w, r)
	}))
	for _, tc := range []struct {
		token, viewer string
		status        int
	}{{b, hostAccountB, 404}, {"", "", 200}, {a, hostAccountA, 200}} {
		before := repo.lookups
		w := hostRequest(h, "GET", "/stargate/accounts/public-profile", tc.token, "")
		if w.Code != tc.status || repo.viewer != tc.viewer || repo.lookups != before+1 {
			t.Fatal("public route lost verified viewer", w.Code, repo.viewer, repo.lookups, w.Body.String())
		}
	}
	before := repo.lookups
	if w := hostRequest(h, "GET", "/api/accounts/public-profile", "invalid", ""); w.Code != 401 || repo.lookups != before {
		t.Fatal("invalid proof downgraded to anonymous", w.Code, repo.lookups)
	}
	store.accounts[hostAccountB].account.Disabled = true
	if w := hostRequest(h, "GET", "/api/accounts/public-profile", b, ""); w.Code != 423 || repo.lookups != before {
		t.Fatal("disabled proof downgraded to anonymous", w.Code, repo.lookups)
	}
}

type publisherIdentityRepository struct{ profileIdentityRepository }

func (r *publisherIdentityRepository) LookupPublisherForViewer(_ context.Context, viewer, ref string) (accounts.Publisher, error) {
	r.viewer = viewer
	r.lookups++
	if viewer != hostAccountA {
		return accounts.Publisher{}, accounts.ErrNotFound
	}
	return accounts.Publisher{ID: hostRoom, Name: "realm-handle"}, nil
}
func (r *publisherIdentityRepository) ListPublishersForViewer(ctx context.Context, viewer, account string) ([]accounts.Publisher, error) {
	p, e := r.LookupPublisherForViewer(ctx, viewer, account)
	return []accounts.Publisher{p}, e
}
func (r *publisherIdentityRepository) ListMembersForViewer(ctx context.Context, viewer, ref string, offset, take int) ([]accounts.Member, int64, error) {
	_, e := r.LookupPublisherForViewer(ctx, viewer, ref)
	return []accounts.Member{}, 0, e
}

func TestPublisherRoutesRetainVerifiedViewerAfterHeaderRemoval(t *testing.T) {
	issuer, verifier, store := hostIdentity(t)
	a, b := hostToken(t, issuer, hostAccountA, "*"), hostToken(t, issuer, hostAccountB, "*")
	repo := &publisherIdentityRepository{}
	h := bindIdentity(verifier, (&accounts.Service{Repository: repo, Authority: proofIdentity{identity{verifier}}}).Handler())
	for _, path := range []string{"/api/publishers/realm-handle", "/api/publishers/of/" + hostAccountA, "/api/publishers/realm-handle/members"} {
		for _, tc := range []struct {
			token, viewer string
			status        int
		}{{a, hostAccountA, 200}, {b, hostAccountB, 404}, {"", "", 404}} {
			before := repo.lookups
			w := hostRequest(h, "GET", path, tc.token, "")
			if w.Code != tc.status || repo.viewer != tc.viewer || repo.lookups != before+1 {
				t.Fatal("publisher route lost verified viewer", path, w.Code, repo.viewer, w.Body.String())
			}
		}
	}
	before := repo.lookups
	if w := hostRequest(h, "GET", "/api/publishers/realm-handle", "invalid", ""); w.Code != 401 || repo.lookups != before {
		t.Fatal("invalid publisher proof downgraded", w.Code)
	}
	store.accounts[hostAccountA].account.Disabled = true
	if w := hostRequest(h, "GET", "/api/publishers/realm-handle", a, ""); w.Code != 423 || repo.lookups != before {
		t.Fatal("disabled publisher proof downgraded", w.Code)
	}
}

func TestAccountPublicRoutesRejectInvalidPresentProof(t *testing.T) {
	issuer, verifier, _ := hostIdentity(t)
	token := hostToken(t, issuer, hostAccountA, "*")
	repo := &publisherIdentityRepository{}
	service := (&accounts.Service{Repository: repo, Authority: proofIdentity{identity{verifier}}}).Handler()
	h := bindIdentity(verifier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("raw bearer reached public handler")
		}
		p := r.Context().Value(principalKey{}).(authority.Principal)
		p.Account.ID = hostAccountB
		service.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}))
	for _, path := range []string{"/api/accounts/public-profile", "/api/publishers/realm-handle", "/api/publishers/of/" + hostAccountA, "/api/publishers/realm-handle/members"} {
		if w := hostRequest(h, "GET", path, token, ""); w.Code != 401 || repo.lookups != 0 {
			t.Fatal("invalid bound proof downgraded to anonymous", path, w.Code, repo.lookups)
		}
	}
}
