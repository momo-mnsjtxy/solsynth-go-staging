// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"errors"
	"time"
)

// RecordedSessionEligible can only keep/revoke an already recorded downstream
// admission. It returns no Principal, proof, token or mutation authority. New
// admissions must independently use original-proof transaction authorization.
// This resource lane accepts ordinary Type-0 sessions only; delegated sessions
// remain restricted to their separately reviewed OAuth binding/route lane.
func (v *Verifier) RecordedSessionEligible(tx Transaction, id string, version, epoch int64, now time.Time, permissions ...string) (bool, error) {
	if v == nil || v.service == nil || tx == nil {
		return false, ErrUnavailable
	}
	if !ValidID(id) || version < 1 || epoch < 1 || len(permissions) == 0 {
		return false, nil
	}
	a := tx.Account()
	if !ValidID(a.ID) || a.Version != version {
		return false, nil
	}
	denial := func(e error) (bool, error) {
		var f *Failure
		if errors.Is(e, ErrNotFound) || errors.As(e, &f) && f.Status != 503 {
			return false, nil
		}
		return false, e
	}
	if e := v.service.accountAllowed(tx, now, false); e != nil {
		return denial(e)
	}
	initial, e := tx.Session(id)
	if e != nil {
		return denial(e)
	}
	if initial.Epoch != epoch {
		return false, nil
	}
	seen := map[string]bool{}
	current := initial
	for depth := 0; ; depth++ {
		if depth >= 64 || seen[current.ID] || current.AccountID != a.ID || current.AccountVersion != version || current.Type != 0 || current.ClientID != "" || current.RevokedAt != nil || !current.ExpiresAt.After(now) || current.CreatedAt.After(now) || current.Epoch < 1 {
			return false, nil
		}
		seen[current.ID] = true
		if len(current.Scopes) == 0 || len(current.Scopes) > 100 {
			return false, nil
		}
		for _, scope := range current.Scopes {
			if !validPattern(scope) {
				return false, nil
			}
		}
		if e = evaluatePolicy(tx, a, current, now, permissions); e != nil {
			return denial(e)
		}
		if current.ParentID == "" {
			break
		}
		current, e = tx.Session(current.ParentID)
		if e != nil {
			return denial(e)
		}
	}
	return true, nil
}
