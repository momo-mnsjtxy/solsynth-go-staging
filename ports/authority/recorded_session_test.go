// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"testing"
	"time"
)

func TestRecordedSessionEligibilityRevocationScopeAndParents(t *testing.T) {
	s, store, clock := setup(t)
	pair := issue(t, s, accountA, "posts.*")
	v, e := NewVerifier(store, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	p, e := v.Authenticate(context.Background(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Unix(clock.Load(), 0).UTC()
	base := cloneState(store.state[accountA])
	base.rules = []Rule{{Pattern: "posts.*"}}
	check := func(state *memState, id string, version, epoch int64, at time.Time, permission string, want bool) {
		t.Helper()
		got, e := v.RecordedSessionEligible(&memTx{state: state}, id, version, epoch, at, permission)
		if e != nil || got != want {
			t.Fatal(got, e, want)
		}
	}
	check(base, p.Session.ID, p.Account.Version, p.Session.Epoch, at, "posts.create", true)
	check(base, p.Session.ID, p.Account.Version, p.Session.Epoch, at, "wallets.pay", false)
	check(base, p.Session.ID, p.Account.Version+1, p.Session.Epoch, at, "posts.create", false)
	check(base, p.Session.ID, p.Account.Version, p.Session.Epoch+1, at, "posts.create", false)
	for _, change := range []func(*memState){
		func(st *memState) { st.rules = nil },
		func(st *memState) { st.rules = append(st.rules, Rule{Pattern: "posts.*", Deny: true}) },
		func(st *memState) {
			st.punishments = []Punishment{{Kind: "permission_modification", Pattern: "posts.*"}}
		},
		func(st *memState) { st.account.Disabled = true },
		func(st *memState) { se := st.sessions[p.Session.ID]; se.RevokedAt = &at; st.sessions[se.ID] = se },
		func(st *memState) {
			se := st.sessions[p.Session.ID]
			se.Type = 1
			se.ClientID = "unsupported-client"
			st.sessions[se.ID] = se
		},
		func(st *memState) {
			se := st.sessions[p.Session.ID]
			se.Scopes = []string{"invalid scope"}
			st.sessions[se.ID] = se
		},
		func(st *memState) { se := st.sessions[p.Session.ID]; se.ParentID = se.ID; st.sessions[se.ID] = se },
	} {
		st := cloneState(base)
		change(st)
		check(st, p.Session.ID, p.Account.Version, p.Session.Epoch, at, "posts.create", false)
	}
	st := cloneState(base)
	child := st.sessions[p.Session.ID]
	parent := child
	parent.ID = "a3000000-0000-4000-8000-000000000099"
	parent.ParentID = ""
	child.ParentID = parent.ID
	st.sessions[parent.ID] = parent
	st.sessions[child.ID] = child
	check(st, child.ID, p.Account.Version, p.Session.Epoch, at, "posts.create", true)
	parent.Scopes = []string{"wallets.*"}
	st.sessions[parent.ID] = parent
	check(st, child.ID, p.Account.Version, p.Session.Epoch, at, "posts.create", false)
	parent.Scopes = []string{"posts.*"}
	parent.ExpiresAt = at
	st.sessions[parent.ID] = parent
	check(st, child.ID, p.Account.Version, p.Session.Epoch, at, "posts.create", false)
	parent.ExpiresAt = at.Add(time.Hour)
	parent.RevokedAt = &at
	st.sessions[parent.ID] = parent
	check(st, child.ID, p.Account.Version, p.Session.Epoch, at, "posts.create", false)
}
