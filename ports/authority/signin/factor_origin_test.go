// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

// This fixture exercises the production EnableFactor path with current fresh
// credential assurance and a separately retained origin session. It deliberately
// models only the persistence methods that path uses; unexpected calls panic.
type factorOriginAuthority struct {
	authority.Transaction
	authority.CredentialAssuranceTransaction
	account authority.Account
	origin  authority.Session
	fresh   authority.Principal
	now     time.Time
	missing bool
}

func (t *factorOriginAuthority) Account() authority.Account { return t.account }
func (t *factorOriginAuthority) SaveAccount(a authority.Account) error {
	t.account = a
	return nil
}
func (t *factorOriginAuthority) Rules() ([]authority.Rule, error)             { return nil, nil }
func (t *factorOriginAuthority) Punishments() ([]authority.Punishment, error) { return nil, nil }
func (t *factorOriginAuthority) Session(id string) (authority.Session, error) {
	if t.missing || id != t.origin.ID {
		return authority.Session{}, authority.ErrNotFound
	}
	return t.origin, nil
}
func (t *factorOriginAuthority) SessionAssurance(id string) (authority.CredentialAssurance, error) {
	if id != t.fresh.Session.ID {
		return authority.CredentialAssurance{}, authority.ErrNotFound
	}
	return authority.CredentialAssurance{SessionID: id, AccountVersion: t.fresh.Account.Version, SessionEpoch: t.fresh.Session.Epoch, AuthTime: t.now, ExpiresAt: t.now.Add(5 * time.Minute), Methods: []string{"pwd"}}, nil
}
func (t *factorOriginAuthority) ConsumeAssurance(string, int64, int64, string, string, int64, string, time.Time) error {
	return nil
}

type factorOriginTransaction struct {
	Transaction
	owner   *factorOriginAuthority
	factor  Factor
	pending PendingEnrollment
	receipt FactorSetup
}

func (t *factorOriginTransaction) Authority() authority.Transaction { return t.owner }
func (t *factorOriginTransaction) Authenticate() (authority.Principal, error) {
	return t.owner.fresh, nil
}
func (t *factorOriginTransaction) Credentials() (Credentials, error) {
	return Credentials{AccountID: t.owner.account.ID, VerifiedAt: &t.owner.now}, nil
}
func (t *factorOriginTransaction) Factors() ([]Factor, error)          { return []Factor{t.factor}, nil }
func (t *factorOriginTransaction) SaveSetupFactor(f Factor) error      { t.factor = f; return nil }
func (t *factorOriginTransaction) FactorSetup() (FactorSetup, error)   { return t.receipt, nil }
func (t *factorOriginTransaction) SaveFactorSetup(v FactorSetup) error { t.receipt = v; return nil }
func (t *factorOriginTransaction) PendingEnrollment(string) (PendingEnrollment, error) {
	return t.pending, nil
}
func (t *factorOriginTransaction) SavePendingEnrollment(v PendingEnrollment) error {
	t.pending = v
	return nil
}
func (t *factorOriginTransaction) SetupPaymentVersion() (int64, error) { return 0, nil }
func (t *factorOriginTransaction) SetupPolicyIdentity() (string, error) {
	return "synthetic-stable-history", nil
}
func (t *factorOriginTransaction) CancelChallenges(string) error { return nil }
func (t *factorOriginTransaction) CancelFactorSetup() error {
	t.pending.Valid = false
	if t.receipt.State == "pending" || t.receipt.State == "recovery" || t.receipt.State == "disabled" {
		t.receipt.State = "cancelled"
	}
	return nil
}

type factorOriginStore struct {
	Store
	tx *factorOriginTransaction
}

func (r *factorOriginStore) Bind(ctx context.Context, _ authority.Principal) context.Context {
	return ctx
}
func (r *factorOriginStore) Within(_ context.Context, _ string, fn func(Transaction) error) error {
	return fn(r.tx)
}
func (r *factorOriginStore) Attempt(context.Context, []byte, int, time.Duration, time.Time) error {
	return nil
}
func (r *factorOriginStore) Authenticate(context.Context, string) (authority.Principal, error) {
	return r.tx.owner.fresh, nil
}

func TestFactorFreshLoginCannotRescueChangedEnrollmentOrigin(t *testing.T) {
	for _, state := range []string{"pending", "disabled"} {
		for _, change := range []string{"none", "refresh", "scope_restore", "shorten_restore", "revoke", "missing"} {
			t.Run(state+"/"+change, func(t *testing.T) {
				now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
				account := authority.Account{ID: "a1100000-0000-4000-8000-000000000001", Version: 2}
				origin := authority.Session{ID: "a1100000-0000-4000-8000-000000000002", AccountID: account.ID, Epoch: 1, AuthorizationEpoch: 1, AccountVersion: account.Version, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Scopes: []string{"*"}}
				if state == "disabled" {
					// Disable itself advances the account version, but does not change
					// the origin's authorization generation or explicitly revoke it.
					origin.AccountVersion--
				}
				fresh := authority.Principal{Account: account, Session: authority.Session{ID: "a1100000-0000-4000-8000-000000000003", AccountID: account.ID, Epoch: 1, AuthorizationEpoch: 1, AccountVersion: account.Version}}
				owner := &factorOriginAuthority{account: account, origin: origin, fresh: fresh, now: now}
				tx := &factorOriginTransaction{owner: owner}
				store := &factorOriginStore{tx: tx}
				s := &Service{store: store, verifier: store, cfg: Config{Issuer: "synthetic", SealKey: bytes.Repeat([]byte{7}, 32), Now: func() time.Time { return now }}}
				end := now.Add(10 * time.Minute)
				seed := []byte("synthetic-totp-seed!!")
				tx.factor = Factor{ID: "a1100000-0000-4000-8000-000000000004", Type: 3, Revision: 2, PendingUntil: &end, LastCounter: -1}
				var e error
				tx.factor.Secret, e = s.seal(seed, account.ID, tx.factor.ID)
				if e != nil {
					t.Fatal(e)
				}
				policy, e := s.factorPolicy(tx, []Factor{tx.factor})
				if e != nil {
					t.Fatal(e)
				}
				tx.pending = PendingEnrollment{FactorID: tx.factor.ID, FactorRevision: tx.factor.Revision, AccountVersion: account.Version, PolicyHash: policy, PendingUntil: end, Valid: true}
				tx.receipt = FactorSetup{SessionID: origin.ID, AccountVersion: account.Version, AuthorizationEpoch: origin.AuthorizationEpoch, AuthTime: now.Add(-6 * time.Minute), ExpiresAt: now.Add(-time.Minute), PolicyHash: policy, TOTPID: tx.factor.ID, TOTPRevision: tx.factor.Revision, State: state}
				write := func(next authority.Session) {
					t.Helper()
					var err error
					owner.origin, err = authority.PrepareSessionWrite(&owner.origin, next)
					if err != nil {
						t.Fatal(err)
					}
				}
				next := owner.origin
				switch change {
				case "refresh":
					next.Epoch++
					next.ExpiresAt = next.ExpiresAt.Add(time.Minute)
					write(next)
				case "scope_restore":
					next.Scopes = []string{"posts.read"}
					write(next)
					next = owner.origin
					next.Scopes = origin.Scopes
					write(next)
				case "shorten_restore":
					next.ExpiresAt = next.ExpiresAt.Add(-time.Minute)
					write(next)
					next = owner.origin
					next.ExpiresAt = origin.ExpiresAt
					write(next)
				case "revoke":
					next.RevokedAt = &now
					write(next)
				case "missing":
					owner.missing = true
				}
				_, e = s.EnableFactor(context.Background(), "synthetic-current-credential", tx.factor.ID, totp(seed, now.Unix()/30))
				if change == "none" || change == "refresh" {
					if e != nil || tx.factor.EnabledAt == nil {
						t.Fatal("ordinary fresh fallback rejected", e)
					}
				} else if !errors.Is(e, ErrInvalid) || tx.factor.EnabledAt != nil || tx.pending.Valid {
					t.Fatal("fresh login rescued security-invalidated origin", e, tx.factor.EnabledAt != nil, tx.pending.Valid)
				}
			})
		}
	}
}
