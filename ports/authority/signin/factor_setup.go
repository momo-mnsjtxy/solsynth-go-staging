// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"sort"
	"time"
)

// Server-held, narrowly scoped setup continuation. It never grants a credential
// mutation outside one recovery prerequisite and one OTP-proven TOTP enrollment.
type FactorSetup struct {
	SessionID                          string
	AccountVersion, AuthorizationEpoch int64
	AuthTime, ExpiresAt                time.Time
	PolicyHash                         string
	RecoveryID, TOTPID                 string
	RecoveryRevision, TOTPRevision     int64
	State                              string
}
type PendingEnrollment struct {
	FactorID                       string
	FactorRevision, AccountVersion int64
	PolicyHash                     string
	PendingUntil                   time.Time
	Valid                          bool
}
type FactorSetupTransaction interface {
	FactorSetup() (FactorSetup, error)
	SaveFactorSetup(FactorSetup) error
	PendingEnrollment(string) (PendingEnrollment, error)
	SavePendingEnrollment(PendingEnrollment) error
	SaveSetupFactor(Factor) error
	CancelFactorSetup() error
	// Includes canonical wallet policy version when that optional schema exists.
	SetupPaymentVersion() (int64, error)
	SetupPolicyIdentity() (string, error)
}

type committedSetupFailure struct{ err error }

func (e committedSetupFailure) Error() string { return e.err.Error() }
func (e committedSetupFailure) Unwrap() error { return e.err }
func rejectSetup(tx FactorSetupTransaction, e error) error {
	if err := tx.CancelFactorSetup(); err != nil {
		return err
	}
	return committedSetupFailure{e}
}
func (s *Service) factorPolicy(tx Transaction, fs []Factor) (string, error) {
	rules, e := tx.Authority().Rules()
	if e != nil {
		return "", e
	}
	punishments, e := tx.Authority().Punishments()
	if e != nil {
		return "", e
	}
	// Sorting encodings makes policy equality independent of SQL result order.
	var policy []string
	for _, r := range rules {
		b, _ := json.Marshal(r)
		policy = append(policy, "rule:"+string(b))
	}
	for _, p := range punishments {
		b, _ := json.Marshal(p)
		policy = append(policy, "punishment:"+string(b))
	}
	for _, f := range fs {
		b, _ := json.Marshal(struct {
			ID       string
			Type     int
			Revision int64
		}{f.ID, f.Type, f.Revision})
		policy = append(policy, "factor:"+string(b))
	}
	setup, ok := tx.(FactorSetupTransaction)
	if !ok {
		return "", authority.ErrUnavailable
	}
	version, e := setup.SetupPaymentVersion()
	if e != nil {
		return "", e
	}
	identity, e := setup.SetupPolicyIdentity()
	if e != nil {
		return "", e
	}
	policy = append(policy, "identity:"+identity)
	sort.Strings(policy)
	b, _ := json.Marshal(struct {
		Issuer  string
		Version int64
		Policy  []string
	}{s.cfg.Issuer, version, policy})
	h := hmac.New(sha256.New, s.cfg.SealKey)
	h.Write([]byte("owned.factor-setup.policy.v1\x00"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Service) freshFactorAssurance(tx Transaction, p authority.Principal) (authority.CredentialAssurance, error) {
	var empty authority.CredentialAssurance
	if p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "" {
		return empty, ErrReauthentication
	}
	if e := s.active(tx, true); e != nil {
		return empty, e
	}
	writer, ok := tx.Authority().(authority.CredentialAssuranceTransaction)
	if !ok {
		return empty, authority.ErrUnavailable
	}
	a, e := writer.SessionAssurance(p.Session.ID)
	if errors.Is(e, authority.ErrNotFound) {
		return empty, ErrReauthentication
	}
	if e != nil {
		return empty, e
	}
	if a.ConsumedAt != nil || a.AccountVersion != p.Account.Version || a.SessionEpoch != p.Session.Epoch || a.SessionID != p.Session.ID || !authority.ValidCredentialMethods(a.Methods) || s.now().Before(a.AuthTime) || !s.now().Before(a.ExpiresAt) {
		return empty, ErrReauthentication
	}
	c, e := tx.Credentials()
	if e != nil {
		return empty, e
	}
	if c.VerifiedAt == nil {
		return empty, ErrUnverified
	}
	return a, nil
}
func (s *Service) consumeFactorAssurance(tx Transaction, p authority.Principal, a authority.CredentialAssurance, action string, f Factor) error {
	if _, e := tx.Authenticate(); e != nil {
		return e
	}
	if e := s.active(tx, true); e != nil {
		return e
	}
	if s.now().Before(a.AuthTime) || !s.now().Before(a.ExpiresAt) {
		return ErrReauthentication
	}
	config := "owned.factor.v1"
	if f.Type == 0 {
		config = "owned.password.v1"
	}
	e := tx.Authority().(authority.CredentialAssuranceTransaction).ConsumeAssurance(p.Session.ID, p.Session.Epoch, p.Account.Version, action, f.ID, f.Revision, config, s.now())
	if errors.Is(e, authority.ErrInvalid) {
		return ErrReauthentication
	}
	return e
}
func (s *Service) setupValid(v FactorSetup, p authority.Principal, policy string) bool {
	return p.Session.Type == 0 && p.Session.ParentID == "" && p.Session.ClientID == "" && v.SessionID == p.Session.ID && v.AccountVersion == p.Account.Version && v.AuthorizationEpoch == p.Session.AuthorizationEpoch && v.PolicyHash == policy && !s.now().Before(v.AuthTime) && s.now().Before(v.ExpiresAt) && (v.State == "recovery" || v.State == "pending")
}
func (s *Service) sensitiveFactor(ctx context.Context, token string, fn func(Transaction, authority.Principal) error) error {
	p, e := s.verifier.Authenticate(ctx, token)
	if e != nil {
		return e
	}
	if e = s.limit(ctx, "factor-mutation", p.Account.ID, 20, 10*time.Minute); e != nil {
		return e
	}
	return s.authenticated(ctx, token, false, func(tx Transaction, p authority.Principal) error {
		if e := s.active(tx, true); e != nil {
			return e
		}
		return fn(tx, p)
	})
}
