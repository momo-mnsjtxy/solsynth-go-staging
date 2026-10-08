// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRefreshPreservesRecordedAuthorizationButRejectsOldAccess(t *testing.T) {
	s, store, clock := setup(t)
	ctx := context.Background()
	store.state[accountA].rules = []Rule{{Pattern: "posts.*"}}
	pair := issue(t, s, accountA, "posts.*")
	v, e := NewVerifier(store, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	p, e := v.Authenticate(ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		previous := pair
		pair, e = s.Refresh(ctx, pair.RefreshToken, 0, "")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = v.Authenticate(ctx, previous.Token); !errors.Is(e, ErrStale) {
			t.Fatal("old access accepted", e)
		}
		se := store.state[accountA].sessions[p.Session.ID]
		if se.Epoch != int64(i+2) || se.AuthorizationEpoch != 1 {
			t.Fatal("generation drift", se)
		}
		at := time.Unix(clock.Load(), 0)
		ok, e := v.RecordedSessionEligible(&memTx{state: store.state[accountA]}, se.ID, p.Account.Version, p.Session.AuthorizationEpoch, at, "posts.create")
		if e != nil || !ok {
			t.Fatal("refresh ended admission", ok, e)
		}
	}
	se := store.state[accountA].sessions[p.Session.ID]
	se.Scopes = []string{"wallets.*"}
	if e = (&memTx{state: store.state[accountA]}).SaveSession(se); e != nil {
		t.Fatal(e)
	}
	se = store.state[accountA].sessions[se.ID]
	se.Scopes = []string{"posts.*"}
	if e = (&memTx{state: store.state[accountA]}).SaveSession(se); e != nil {
		t.Fatal(e)
	}
	se = store.state[accountA].sessions[se.ID]
	ok, e := v.RecordedSessionEligible(&memTx{state: store.state[accountA]}, se.ID, p.Account.Version, p.Session.AuthorizationEpoch, time.Unix(clock.Load(), 0), "posts.create")
	if e != nil || ok || se.AuthorizationEpoch != 3 {
		t.Fatal("scope restoration revived old admission", ok, e)
	}
	b, e := json.Marshal(se)
	if e != nil || strings.Contains(string(b), "epoch") {
		t.Fatal("internal generation leaked", string(b), e)
	}
}

func TestSessionGenerationTransitionAndAuthenticationLane(t *testing.T) {
	s, store, clock := setup(t)
	pair := issue(t, s, accountA, "*")
	p, e := s.Authenticate(context.Background(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	v, e := NewVerifier(store, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	tx := &memTx{state: store.state[accountA]}
	at := time.Unix(clock.Load(), 0)
	if ok, e := v.RecordedAuthenticationEligible(tx, p.Session.ID, p.Account.Version, 1, at); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if ok, e := v.RecordedSessionEligible(tx, p.Session.ID, p.Account.Version, 1, at); e != nil || ok {
		t.Fatal("empty permission acquired grant", ok, e)
	}
	for _, mutate := range []func(*Session){
		func(s *Session) { s.AuthorizationEpoch = 0 },
		func(s *Session) { s.AuthorizationEpoch = s.Epoch + 1 },
		func(s *Session) { s.ParentID = accountB },
		func(s *Session) { s.Type = 1; s.ClientID = "unbound" },
	} {
		next := p.Session
		mutate(&next)
		if _, e := PrepareSessionWrite(&p.Session, next); !errors.Is(e, ErrInvalid) {
			t.Fatal("unsafe transition", e)
		}
	}
	if e = s.Revoke(context.Background(), pair.Token, "current"); e != nil {
		t.Fatal(e)
	}
	se := store.state[accountA].sessions[p.Session.ID]
	if se.Epoch != 2 || se.AuthorizationEpoch != 2 {
		t.Fatal("revoke did not advance both generations", se)
	}
	if ok, e := v.RecordedAuthenticationEligible(&memTx{state: store.state[accountA]}, se.ID, p.Account.Version, 1, at); e != nil || ok {
		t.Fatal("revoked admission survived", ok, e)
	}
}
