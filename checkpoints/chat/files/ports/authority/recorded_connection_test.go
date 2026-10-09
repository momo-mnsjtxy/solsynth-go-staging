// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"testing"
	"time"
)

type connectionBindingTx struct {
	*memTx
	denied error
	calls  int
}

func (t *connectionBindingTx) CheckSessionBinding(s Session, now time.Time) error {
	t.calls++
	return t.denied
}
func TestRecordedConnectionDelegatedBindingAndRootIsolation(t *testing.T) {
	s, store, clock := setup(t)
	pair := issue(t, s, accountA, "*")
	p, e := s.Authenticate(t.Context(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	v, e := NewVerifier(store, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Unix(clock.Load(), 0)
	state := cloneState(store.state[accountA])
	parent := state.sessions[p.Session.ID]
	delegated := parent
	delegated.ID = "a3000000-0000-4000-8000-000000000097"
	delegated.ParentID = parent.ID
	delegated.Type = 1
	delegated.Category = OAuthSessionCategory
	delegated.ClientID = accountB
	delegated.Scopes = []string{"openid"}
	state.sessions[delegated.ID] = delegated
	tx := &connectionBindingTx{memTx: &memTx{state: state}}
	ok, e := v.RecordedConnectionEligible(tx, delegated.ID, p.Account.Version, delegated.AuthorizationEpoch, at)
	if e != nil || !ok || tx.calls != 1 {
		t.Fatal("valid delegated binding", ok, e, tx.calls)
	}
	if ok, e = v.RecordedAuthenticationEligible(tx, delegated.ID, p.Account.Version, delegated.AuthorizationEpoch, at); e != nil || ok {
		t.Fatal("expanded resource lane", ok, e)
	}
	if ok, e = v.RecordedSessionEligible(tx, delegated.ID, p.Account.Version, delegated.AuthorizationEpoch, at, "chat.messages.create"); e != nil || ok {
		t.Fatal("promoted scoped session", ok, e)
	}
	tx.denied = ErrInvalid
	if ok, e = v.RecordedConnectionEligible(tx, delegated.ID, p.Account.Version, delegated.AuthorizationEpoch, at); e != nil || ok {
		t.Fatal("withdrawn app binding", ok, e)
	}
	tx.denied = ErrUnavailable
	if ok, e = v.RecordedConnectionEligible(tx, delegated.ID, p.Account.Version, delegated.AuthorizationEpoch, at); e != ErrUnavailable || ok {
		t.Fatal("binding failure swallowed", ok, e)
	}
	tx.denied = nil
	parent.RevokedAt = &at
	state.sessions[parent.ID] = parent
	if ok, e = v.RecordedConnectionEligible(tx, delegated.ID, p.Account.Version, delegated.AuthorizationEpoch, at); e != nil || ok {
		t.Fatal("revoked parent", ok, e)
	}
	if ok, e = v.RecordedConnectionEligible(&memTx{state: state}, delegated.ID, p.Account.Version, delegated.AuthorizationEpoch, at); e != nil || ok {
		t.Fatal("missing delegated binding", ok, e)
	}
}
