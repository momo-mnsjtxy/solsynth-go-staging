// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"errors"
	"testing"
	"time"
)

type sudoTestTransaction struct {
	*memTx
	assurance SudoElevation
	readError error
}

func (*sudoTestTransaction) SaveCodeEvidence([]byte, CredentialEvidence) error { return ErrUnavailable }
func (*sudoTestTransaction) CodeEvidence([]byte) (*CredentialEvidence, error) {
	return nil, ErrUnavailable
}
func (*sudoTestTransaction) SaveSessionAssurance(Session, []byte, CredentialEvidence, time.Time) error {
	return ErrUnavailable
}
func (t *sudoTestTransaction) SudoElevation(string) (SudoElevation, error) {
	return t.assurance, t.readError
}
func (*sudoTestTransaction) ConsumeAssurance(string, int64, int64, string, string, int64, string, time.Time) error {
	return ErrUnavailable
}

func TestSudoRequiresCanonicalCredentialEvidenceAndOriginalProof(t *testing.T) {
	s, store, clock := setup(t)
	pair := issue(t, s, accountA, "*")
	v, err := NewVerifier(store, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Authenticate(context.Background(), pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(clock.Load(), 0)
	base := SudoElevation{TrustPoints: 2, SessionID: p.Session.ID, AccountVersion: p.Account.Version, SessionEpoch: p.Session.Epoch, AuthTime: now, ExpiresAt: now.Add(5 * time.Minute), Methods: []string{"pwd", "otp"}}
	for _, test := range []string{"valid", "missing", "backend_error", "no_reader", "expired", "future", "overlong", "trust", "session", "epoch", "account", "methods", "forged", "revoked"} {
		t.Run(test, func(t *testing.T) {
			tx := &sudoTestTransaction{memTx: &memTx{state: cloneState(store.state[accountA])}, assurance: base}
			proof := p
			want := error(ErrSudoRequired)
			switch test {
			case "valid":
				want = nil
			case "missing":
				tx.readError = ErrNotFound
			case "backend_error":
				tx.readError = errors.New("synthetic storage error")
				want = ErrSudoUnavailable
			case "no_reader":
				_, err := v.RequireSudoTransaction(context.Background(), p, tx.memTx)
				if !errors.Is(err, ErrSudoUnavailable) {
					t.Fatal(err)
				}
				return
			case "expired":
				tx.assurance.ExpiresAt = now
			case "future":
				tx.assurance.AuthTime = now.Add(time.Second)
			case "overlong":
				tx.assurance.AuthTime = now.Add(-5 * time.Minute)
				tx.assurance.ExpiresAt = now.Add(time.Hour)
			case "trust":
				tx.assurance.TrustPoints = 1
			case "session":
				tx.assurance.SessionID = "another"
			case "epoch":
				tx.assurance.SessionEpoch++
			case "account":
				tx.assurance.AccountVersion++
			case "methods":
				tx.assurance.Methods = []string{"otp"}
			case "forged":
				proof.Session.Trusted = !proof.Session.Trusted
				want = ErrInvalid
			case "revoked":
				se := tx.state.sessions[p.Session.ID]
				se.RevokedAt = &now
				tx.state.sessions[p.Session.ID] = se
				want = ErrRevoked
			}
			_, err := v.RequireSudoTransaction(context.Background(), proof, tx)
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}

func TestOAuthWildcardNeverBypassesAccountPolicy(t *testing.T) {
	s, store, _ := setup(t)
	pair := issue(t, s, accountA, "*")
	if _, err := s.Authorize(context.Background(), pair.Token, "admin.wallets.orders.view"); !errors.Is(err, ErrForbidden) {
		t.Fatal("login wildcard bypassed canonical grants", err)
	}
	store.state[accountA].rules = []Rule{{Pattern: "*"}, {Pattern: "admin.wallets.orders.view", Deny: true}}
	if _, err := s.Authorize(context.Background(), pair.Token, "admin.wallets.orders.view"); !errors.Is(err, ErrForbidden) {
		t.Fatal("wildcard bypassed deny", err)
	}
	for _, test := range []struct {
		scopes   []string
		required string
		want     bool
	}{
		{[]string{"*"}, "posts.view", true}, {[]string{"posts.*"}, "posts.view", true}, {[]string{"posts.view"}, "posts.view", true}, {[]string{"posts.*"}, "chat.view", false}, {[]string{"posts.*"}, "postsview", false}, {[]string{"post*"}, "posts.view", false}, {[]string{"*"}, "*", false},
	} {
		se := Session{Type: 1, Category: OAuthSessionCategory, Scopes: test.scopes}
		if got := OAuthResourceScope(se, test.required); got != test.want {
			t.Fatal(se, test, got)
		}
		se.Type = 0
		if OAuthResourceScope(se, test.required) {
			t.Fatal("non-OAuth wildcard admitted")
		}
	}
}

func TestSudoRejectsDelegatedAndNonInteractiveSessions(t *testing.T) {
	for _, session := range []Session{
		{Type: 1, Category: OAuthSessionCategory},
		{Type: 2, Category: "api-key"},
		{Type: 0, Category: "device", ClientID: "client"},
		{Type: 0, Category: "device", ParentID: "parent"},
		{Type: 0, Category: "api-key"},
	} {
		if sudoFirstPartySession(session) {
			t.Fatal("non-interactive or delegated credential admitted", session)
		}
	}
}

func TestSudoRefreshDoesNotRenewCredentialEvidence(t *testing.T) {
	s, store, clock := setup(t)
	ctx := context.Background()
	pair := issue(t, s, accountA, "*")
	before, err := s.Authenticate(ctx, pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(clock.Load(), 0)
	assurance := SudoElevation{TrustPoints: 2, SessionID: before.Session.ID, AccountVersion: before.Account.Version, SessionEpoch: before.Session.Epoch, AuthTime: at, ExpiresAt: at.Add(5 * time.Minute), Methods: []string{"pwd", "email"}}
	clock.Add(1)
	refreshed, err := s.Refresh(ctx, pair.RefreshToken, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(store, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := v.Authenticate(ctx, refreshed.Token)
	if err != nil {
		t.Fatal(err)
	}
	tx := &sudoTestTransaction{memTx: &memTx{state: cloneState(store.state[accountA])}, assurance: assurance}
	if _, err := v.RequireSudoTransaction(ctx, p, tx); !errors.Is(err, ErrSudoRequired) {
		t.Fatal("refresh replaced re-authentication", err)
	}
}
