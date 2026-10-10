// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"errors"
	"testing"
	"time"
)

type browserTicketStore struct {
	*memStore
	proof   Principal
	issued  time.Time
	redeems int
}

func (s *browserTicketStore) RedeemBrowserTicket(_ context.Context, hash []byte) (actor, session string, version, sessionEpoch, authorizationEpoch int64, use string, issuedAt, expiresAt time.Time, err error) {
	if len(hash) != 32 || s.redeems != 0 {
		err = ErrExpired
		return
	}
	s.redeems++
	p := s.proof
	return p.Account.ID, p.Session.ID, p.Account.Version, p.Session.Epoch, p.Session.AuthorizationEpoch, "user", s.issued, s.issued.Add(30 * time.Second), nil
}

func TestResumeBrowserTicketRechecksRecordedGenerations(t *testing.T) {
	service, store, clock := setup(t)
	pair, err := service.IssueSession(context.Background(), accountA, SessionOptions{Label: "browser", Scopes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := service.Authenticate(context.Background(), pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	v := &Verifier{service: service}
	issued := time.Unix(clock.Load(), 0).UTC()
	resumed, err := v.resumeBrowserTicket(context.Background(), proof.Account.ID, proof.Session.ID, proof.Account.Version, proof.Session.Epoch, proof.Session.AuthorizationEpoch, "user", issued, issued.Add(30*time.Second))
	if err != nil || resumed.Account.ID != proof.Account.ID {
		t.Fatal(err, resumed.Account.ID)
	}
	shared := &browserTicketStore{memStore: store, proof: proof, issued: issued}
	sharedVerifier, err := NewVerifier(shared, service.cfg)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err = sharedVerifier.RedeemBrowserTicket(context.Background(), make([]byte, 32))
	if err != nil || resumed.Account.ID != proof.Account.ID || shared.redeems != 1 {
		t.Fatal("shared redemption did not use verifier read store", resumed, shared.redeems, err)
	}
	store.mu.Lock()
	s := store.state[accountA].sessions[proof.Session.ID]
	s.AuthorizationEpoch++
	s.Epoch++
	store.state[accountA].sessions[proof.Session.ID] = s
	store.mu.Unlock()
	if _, err = v.resumeBrowserTicket(context.Background(), proof.Account.ID, proof.Session.ID, proof.Account.Version, proof.Session.Epoch, proof.Session.AuthorizationEpoch, "user", issued, issued.Add(30*time.Second)); !errors.Is(err, ErrStale) {
		t.Fatal("stale ticket resumed", err)
	}
	if _, err = v.RedeemBrowserTicket(context.Background(), make([]byte, 32)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("store without ticket redemption capability accepted", err)
	}
}
