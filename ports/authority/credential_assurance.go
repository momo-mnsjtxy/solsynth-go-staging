// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"time"
)

type CredentialEvidence struct {
	AuthTime time.Time
	Methods  []string
}
type CredentialAssurance struct {
	SessionID                    string
	AccountVersion, SessionEpoch int64
	AuthTime, ExpiresAt          time.Time
	Methods                      []string
	ConsumedAt                   *time.Time
	Action, TargetID, ConfigID   string
	PolicyVersion                *int64
}
type CredentialAssuranceTransaction interface {
	SaveCodeEvidence([]byte, CredentialEvidence) error
	CodeEvidence([]byte) (*CredentialEvidence, error)
	SaveSessionAssurance(Session, []byte, CredentialEvidence, time.Time) error
	SessionAssurance(string) (CredentialAssurance, error)
	ConsumeAssurance(string, int64, int64, string, string, int64, string, time.Time) error
}

func ValidCredentialMethods(methods []string) bool {
	if len(methods) < 1 || len(methods) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, m := range methods {
		if seen[m] || m != "pwd" && m != "otp" && m != "recovery" {
			return false
		}
		seen[m] = true
	}
	return seen["pwd"] && !(seen["otp"] && seen["recovery"])
}

// Only canonical completed password + required second-factor verification calls
// this trusted hook. No HTTP input can set the evidence or authentication time.
func (s *Service) CreateVerifiedLoginCodeTransaction(ctx context.Context, tx Transaction, raw, label string, scopes, methods []string, authTime time.Time) error {
	if s == nil || tx == nil || ctx.Err() != nil || !ValidCredentialMethods(methods) {
		return ErrInvalid
	}
	now := s.cfg.Now().UTC()
	if authTime.After(now) || now.Sub(authTime) > 5*time.Minute {
		return ErrInvalid
	}
	writer, ok := tx.(CredentialAssuranceTransaction)
	if !ok {
		return ErrUnavailable
	}
	if e := s.CreateLoginCodeTransaction(ctx, tx, raw, label, scopes); e != nil {
		return e
	}
	return writer.SaveCodeEvidence(s.digest(raw), CredentialEvidence{authTime.UTC().Truncate(time.Microsecond), append([]string(nil), methods...)})
}
