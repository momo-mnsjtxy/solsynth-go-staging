// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"testing"

	"solsynth.local/go-migration/ports/passport/passkit"
)

func TestPasskitProvisioningRejectsScopedAndDelegatedProof(t *testing.T) {
	issuer, verifier, store := hostIdentity(t)
	owner := hostToken(t, issuer, hostAccountA, "accounts.manage")
	parent, e := issuer.Authenticate(context.Background(), owner)
	if e != nil {
		t.Fatal(e)
	}
	repo := &passkitHostRepository{bundle: passkit.Bundle{Data: []byte("synthetic-test-bundle")}}
	service := (&passkit.Service{Repository: repo, Authority: proofIdentity{identity{verifier}}}).Handler()
	h := router{modules{passkit: passkitModules{member: bindIdentity(verifier, service), callbacks: service}}}
	if w := hostRequest(h, "GET", "/passport/accounts/me/passbook/member", owner, ""); w.Code != 200 || repo.calls != 1 {
		t.Fatal("authorized first-party owner denied", w.Code)
	}
	narrow := hostToken(t, issuer, hostAccountA, "posts.read")
	delegated := hostToken(t, issuer, hostAccountA, "accounts.manage")
	child, e := issuer.Authenticate(context.Background(), delegated)
	if e != nil {
		t.Fatal(e)
	}
	row := store.accounts[hostAccountA].sessions[child.Session.ID]
	row.ParentID = parent.Session.ID
	store.accounts[hostAccountA].sessions[row.ID] = row
	for _, token := range []string{narrow, delegated} {
		before := repo.calls
		if w := hostRequest(h, "GET", "/api/accounts/me/passbook/member", token, ""); w.Code != 403 || repo.calls != before {
			t.Fatal("limited/delegated proof acquired persistent pass token", w.Code, repo.calls)
		}
	}
}
