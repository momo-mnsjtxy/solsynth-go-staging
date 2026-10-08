// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTransactionVerificationPreservesProofAndPermissionBoundary(t *testing.T) {
	s, store, now := setup(t)
	pair := issue(t, s, accountA, "posts.*")
	v, err := NewVerifier(store, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Authenticate(context.Background(), pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	state := cloneState(store.state[accountA])
	state.rules = []Rule{{Pattern: "posts.*"}, {Pattern: "wallets.*"}}
	tx := &memTx{state: state}
	ctx := context.Background()
	fresh, err := v.AuthorizeTransaction(ctx, p, tx, "posts.create")
	if err != nil || fresh.Account.ID != accountA {
		t.Fatal(fresh, err)
	}
	if fresh.proof != nil || fresh.Session.RefreshHash != nil || fresh.Session.PreviousHash != nil || fresh.Session.SealedPair != nil {
		t.Fatal("transaction projection retained authority capability or credential material")
	}
	if _, err = v.AuthorizeTransaction(ctx, p, tx); !errors.Is(err, ErrForbidden) {
		t.Fatal("empty permissions", err)
	}
	if _, err = v.AuthorizeTransaction(ctx, p, tx, "wallets.transactions.manage"); !errors.Is(err, ErrForbidden) {
		t.Fatal("scope widened", err)
	}
	if _, err = v.AuthorizeTransaction(ctx, p, tx, "posts.*"); !errors.Is(err, ErrForbidden) {
		t.Fatal("wildcard permission accepted", err)
	}
	if _, err = v.AuthenticateTransaction(ctx, Principal{Account: p.Account, Session: p.Session}, tx); !errors.Is(err, ErrInvalid) {
		t.Fatal("forged projection", err)
	}
	changed := p
	changed.Account.Superuser = true
	if _, err = v.AuthenticateTransaction(ctx, changed, tx); !errors.Is(err, ErrInvalid) {
		t.Fatal("altered projection", err)
	}
	otherVerifier, err := NewVerifier(store, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = otherVerifier.AuthenticateTransaction(ctx, p, tx); !errors.Is(err, ErrInvalid) {
		t.Fatal("different verifier", err)
	}
	state.rules = nil
	if _, err = v.AuthorizeTransaction(ctx, p, tx, "posts.create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale grant", err)
	}
	if _, err = v.AuthenticateTransaction(ctx, p, tx); err != nil {
		t.Fatal("authentication-only route gained permission requirement", err)
	}
	state.rules = []Rule{{Pattern: "posts.*"}}
	state.punishments = []Punishment{{Kind: "permission_modification", Pattern: "posts.*"}}
	if _, err = v.AuthorizeTransaction(ctx, p, tx, "posts.create"); !errors.Is(err, ErrForbidden) {
		t.Fatal("fresh punishment", err)
	}
	state.punishments = nil
	state.account.Disabled = true
	if _, err = v.AuthenticateTransaction(ctx, p, tx); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled", err)
	}
	state.account.Disabled = false
	state.account.Version++
	if _, err = v.AuthenticateTransaction(ctx, p, tx); !errors.Is(err, ErrStale) {
		t.Fatal("account version", err)
	}
	state.account.Version--
	se := state.sessions[p.Session.ID]
	at := time.Unix(now.Load(), 0)
	se.RevokedAt = &at
	state.sessions[p.Session.ID] = se
	if _, err = v.AuthenticateTransaction(ctx, p, tx); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked", err)
	}
	se.RevokedAt = nil
	se.Epoch++
	state.sessions[p.Session.ID] = se
	if _, err = v.AuthenticateTransaction(ctx, p, tx); !errors.Is(err, ErrStale) {
		t.Fatal("rotated epoch", err)
	}
	se.Epoch--
	state.sessions[p.Session.ID] = se
	now.Add(600)
	if _, err = v.AuthenticateTransaction(ctx, p, tx); !errors.Is(err, ErrExpired) {
		t.Fatal("expiry", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = v.AuthenticateTransaction(ctx, p, tx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("cancellation", err)
	}
}
