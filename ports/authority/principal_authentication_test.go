// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuthenticatePrincipalFreshnessAndProof(t *testing.T) {
	service, store, _ := setup(t)
	pair, err := service.IssueSession(context.Background(), accountA, SessionOptions{Label: "auth-only", Scopes: []string{"unrelated.scope"}})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := service.Authenticate(context.Background(), pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AuthenticatePrincipal(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AuthorizePrincipal(context.Background(), proof); !errors.Is(err, ErrForbidden) {
		t.Fatal("empty permission authorization changed", err)
	}
	if _, err = service.AuthorizePrincipal(context.Background(), proof, "accounts.view"); !errors.Is(err, ErrForbidden) {
		t.Fatal("auth-only conferred permission", err)
	}
	bad := proof
	bad.Account.Name = "mutated"
	if _, err = service.AuthenticatePrincipal(context.Background(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = service.AuthenticatePrincipal(context.Background(), Principal{Account: proof.Account, Session: proof.Session}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	other, _, _ := setup(t)
	if _, err = other.AuthenticatePrincipal(context.Background(), proof); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-instance proof", err)
	}
	verifier, err := NewVerifier(store, service.cfg)
	if err != nil {
		t.Fatal(err)
	}
	vp, err := verifier.Authenticate(context.Background(), pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifier.AuthenticatePrincipal(context.Background(), vp); err != nil {
		t.Fatal(err)
	}
	if _, err = verifier.AuthenticatePrincipal(context.Background(), proof); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	store.mu.Lock()
	s := store.state[accountA].sessions[proof.Session.ID]
	at := time.Now()
	s.RevokedAt = &at
	store.state[accountA].sessions[s.ID] = s
	store.mu.Unlock()
	if _, err = service.AuthenticatePrincipal(context.Background(), proof); !errors.Is(err, ErrRevoked) {
		t.Fatal("revocation not fresh", err)
	}
	if _, err = verifier.AuthenticatePrincipal(context.Background(), vp); !errors.Is(err, ErrRevoked) {
		t.Fatal("verifier revocation not fresh", err)
	}
}
