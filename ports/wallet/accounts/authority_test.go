package accounts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This synthetic verifier records bridge behavior only; actual RSA/database
// authority tests belong to ports/authority and the cross-family PG test.
type proofFixture struct {
	owner            *proofIdentity
	account, session string
}
type proofIdentity struct {
	revoked bool
	checks  int
}

func (i *proofIdentity) AuthenticateRequest(*http.Request) (proofFixture, error) {
	return proofFixture{i, accountA, "synthetic-session"}, nil
}
func (i *proofIdentity) AuthenticatePrincipal(ctx context.Context, p proofFixture) (proofFixture, error) {
	return i.check(p)
}
func (i *proofIdentity) AuthorizePrincipal(_ context.Context, p proofFixture, keys ...string) (proofFixture, error) {
	if len(keys) == 0 {
		return proofFixture{}, ErrForbidden
	}
	return i.check(p)
}
func (i *proofIdentity) check(p proofFixture) (proofFixture, error) {
	i.checks++
	if p.owner != i || i.revoked {
		return proofFixture{}, ErrUnauthorized
	}
	return p, nil
}
func projectProof(p proofFixture) Principal { return Principal{p.account, p.session} }
func TestIdentityBridgeProofIsolationAndFreshness(t *testing.T) {
	id := &proofIdentity{}
	bridge := &identityBridge[proofFixture]{identity: id, project: projectProof}
	req := httptest.NewRequest("GET", "/api/wallets", nil)
	principal, e := bridge.Authenticate(req)
	if e != nil {
		t.Fatal(e)
	}
	if e = bridge.Permission(req.Context(), principal, PermissionTransactions); e != nil || id.checks != 1 {
		t.Fatal(e)
	}
	bridge.realms = &accessFixture{}
	if ids, e := bridge.RealmIDs(req.Context(), principal); e != nil || len(ids) != 0 {
		t.Fatal("auth-only list incorrectly required a permission", ids, e)
	}
	other := &identityBridge[proofFixture]{identity: id, project: projectProof}
	if e = other.Permission(req.Context(), principal, PermissionTransactions); e != ErrUnauthorized {
		t.Fatal("cross-bridge proof accepted")
	}
	principal.AccountID = accountB
	if e = bridge.Permission(req.Context(), principal, PermissionTransactions); e != ErrUnauthorized {
		t.Fatal("altered projection accepted")
	}
	id.revoked = true
	if e = bridge.Permission(req.Context(), projectProof(proofFixture{id, accountA, "synthetic-session"}), PermissionTransactions); e != ErrUnauthorized {
		t.Fatal("stale proof accepted")
	}
	if e = bridge.Permission(context.Background(), Principal{accountA, "synthetic-session"}, PermissionTransactions); e != ErrUnauthorized {
		t.Fatal("request-free principal accepted")
	}
}
