// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"time"
)

// ValidPermissionPattern exposes the authority's existing policy grammar to
// trusted configuration validators; it does not itself grant any permission.
func ValidPermissionPattern(pattern string) bool { return validPattern(pattern) }

// CreateLoginCodeTransaction is a trusted first-party login-completion hook.
// The adapter must retain the canonical account lock until commit, verify all
// factors on a server-generated challenge, and pass its random bearer ID here.
// This method does not commit and cannot authenticate a caller by itself.
func (s *Service) CreateLoginCodeTransaction(ctx context.Context, tx Transaction, raw, label string, scopes []string) error {
	if s == nil || tx == nil || ctx.Err() != nil || !validSecret(raw) || len(label) > 200 || len(scopes) == 0 || len(scopes) > 100 {
		return ErrInvalid
	}
	for _, scope := range scopes {
		if !validPattern(scope) {
			return ErrInvalid
		}
	}
	now := s.cfg.Now().UTC()
	if e := s.accountAllowed(tx, now, true); e != nil {
		return known(e)
	}
	return known(tx.SaveCode(LoginCode{Hash: s.digest(raw), AccountID: tx.Account().ID, AccountVersion: tx.Account().Version, ExpiresAt: now.Add(time.Minute), Label: label, Scopes: append([]string(nil), scopes...)}))
}
