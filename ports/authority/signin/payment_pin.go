// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

type PaymentPINTransaction interface {
	PaymentPINVersion() (int64, error)
	PaymentPINConfig(string) (string, error)
	MutatePaymentPIN(string, Factor, string, time.Time) error
}

func validPINAction(action, target string) bool {
	if action == "create" {
		return target == ""
	}
	return (action == "enable" || action == "disable" || action == "delete") && authority.ValidID(target)
}
func sixDigitPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func (s *Service) isPaymentPIN(ctx context.Context, token, fid string) (bool, error) {
	if !authority.ValidID(fid) {
		return false, ErrInput
	}
	fs, e := s.Factors(ctx, token)
	if e != nil {
		return false, e
	}
	for _, f := range fs {
		if f.ID == fid {
			return f.Type == 4, nil
		}
	}
	return false, ErrInvalid
}

// The authored bearer/body shape consumes only actual credential assurance.
// There is no new required header or route, and refresh is never reauthentication.
func (s *Service) mutatePaymentPIN(ctx context.Context, token, action, fid, pin string) (Factor, error) {
	var out Factor
	if !validPINAction(action, fid) || ((action == "create" || action == "enable") && !sixDigitPIN(pin)) {
		return out, ErrInput
	}
	p, e := s.verifier.Authenticate(ctx, token)
	if e != nil {
		return out, e
	}
	if e = s.limit(ctx, "pin-lifecycle", p.Account.ID, 5, 15*time.Minute); e != nil {
		return out, e
	}
	e = s.authenticated(ctx, token, false, func(tx Transaction, p authority.Principal) error {
		if p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "" {
			return ErrReauthentication
		}
		if e := s.active(tx, true); e != nil {
			return e
		}
		assurances, ok := tx.Authority().(authority.CredentialAssuranceTransaction)
		if !ok {
			return authority.ErrUnavailable
		}
		assurance, e := assurances.SessionAssurance(p.Session.ID)
		if errors.Is(e, authority.ErrNotFound) {
			return ErrReauthentication
		}
		if e != nil {
			return e
		}
		now := s.now()
		if assurance.ConsumedAt != nil || assurance.SessionID != p.Session.ID || assurance.AccountVersion != p.Account.Version || assurance.SessionEpoch != p.Session.Epoch || !authority.ValidCredentialMethods(assurance.Methods) || now.Before(assurance.AuthTime) || !now.Before(assurance.ExpiresAt) {
			return ErrReauthentication
		}
		credentials, e := tx.Credentials()
		if e != nil {
			return e
		}
		if credentials.VerifiedAt == nil {
			return ErrUnverified
		}
		adapter, ok := tx.(PaymentPINTransaction)
		if !ok {
			return ErrUnsupported
		}
		version, e := adapter.PaymentPINVersion()
		if e != nil {
			return e
		}
		config, e := adapter.PaymentPINConfig(action)
		if e != nil {
			return e
		}
		fs, e := tx.Factors()
		if e != nil {
			return e
		}
		var f Factor
		found := false
		for _, v := range fs {
			if v.Type == 4 {
				f = v
				found = true
				break
			}
		}
		if action == "create" {
			if found {
				return ErrConflict
			}
			id, e := newID()
			if e != nil {
				return e
			}
			f = Factor{ID: id, Type: 4, Trustworthy: 0, CreatedAt: now, EnabledAt: &now, LastCounter: -1}
		} else if !found || f.ID != fid {
			return ErrInvalid
		}
		if action == "enable" {
			if f.EnabledAt != nil {
				return ErrConflict
			}
			f.EnabledAt = &now
		}
		if action == "disable" {
			if f.EnabledAt == nil {
				return ErrConflict
			}
			f.EnabledAt = nil
		}
		if action != "delete" {
			if e = tx.SaveFactor(f); e != nil {
				return e
			}
		}
		if e = adapter.MutatePaymentPIN(action, f, pin, now); e != nil {
			return e
		}
		if action == "delete" {
			if e = tx.DeleteFactor(f.ID); e != nil {
				return e
			}
		}
		if _, e = tx.Authenticate(); e != nil {
			return e
		}
		if e = s.active(tx, true); e != nil {
			return e
		}
		if s.now().Before(assurance.AuthTime) || !s.now().Before(assurance.ExpiresAt) {
			return ErrReauthentication
		}
		if e = assurances.ConsumeAssurance(p.Session.ID, p.Session.Epoch, p.Account.Version, action, f.ID, version, config, s.now()); e != nil {
			if errors.Is(e, authority.ErrInvalid) {
				return ErrReauthentication
			}
			return e
		}
		out = cleanFactor(f)
		return s.invalidate(tx)
	})
	return out, e
}
