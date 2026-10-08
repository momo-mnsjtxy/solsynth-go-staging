// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"math"
	"slices"
)

// PrepareSessionWrite is the shared persistence boundary for session generations.
// Epoch invalidates access tokens and short-lived assurance. AuthorizationEpoch
// invalidates durable admissions. A routine token rotation changes only Epoch.
// Stores must call this while holding the canonical account transaction lock.
func PrepareSessionWrite(previous *Session, next Session) (Session, error) {
	if !ValidID(next.ID) || !ValidID(next.AccountID) || next.Epoch < 1 || next.AuthorizationEpoch < 1 || next.AuthorizationEpoch > next.Epoch || next.AccountVersion < 1 {
		return Session{}, ErrInvalid
	}
	if previous == nil {
		return next, nil
	}
	p := *previous
	if p.Epoch < 1 || p.AuthorizationEpoch < 1 || p.AuthorizationEpoch > p.Epoch {
		return Session{}, ErrInvalid
	}
	if next.ID != p.ID || next.AccountID != p.AccountID || next.ParentID != p.ParentID || next.Type != p.Type || next.ClientID != p.ClientID || next.AccountVersion != p.AccountVersion || !next.CreatedAt.Equal(p.CreatedAt) || next.Category != p.Category || next.Trusted != p.Trusted || next.Epoch < p.Epoch || next.AuthorizationEpoch < p.AuthorizationEpoch || p.RevokedAt != nil && next.RevokedAt == nil {
		return Session{}, ErrInvalid
	}
	// Conservatively invalidate on every scope change, including restoration,
	// or a shortened expiry. Reordering scopes can invalidate but never revive.
	if !slices.Equal(p.Scopes, next.Scopes) || next.ExpiresAt.Before(p.ExpiresAt) || p.RevokedAt == nil && next.RevokedAt != nil {
		if p.Epoch == math.MaxInt64 || p.AuthorizationEpoch == math.MaxInt64 {
			return Session{}, ErrInvalid
		}
		next.Epoch = max(next.Epoch, p.Epoch+1)
		next.AuthorizationEpoch = max(next.AuthorizationEpoch, p.AuthorizationEpoch+1)
	}
	return next, nil
}
