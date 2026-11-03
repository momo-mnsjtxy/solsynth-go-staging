// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"errors"
	"time"
)

var (
	ErrSudoRequired    = &Failure{"AUTH_SUDO_REQUIRED", "This action requires re-authentication.", 403}
	ErrSudoUnavailable = &Failure{"AUTH_SUDO_UNAVAILABLE", "Re-authentication could not be verified.", 503}
)

type SudoElevation struct {
	SessionID                    string
	AccountVersion, SessionEpoch int64
	AuthTime, ExpiresAt          time.Time
	Methods                      []string
	TrustPoints                  int
}

type SudoElevationReader interface {
	SudoElevation(string) (SudoElevation, error)
}

func ValidSudoMethods(methods []string) bool {
	if len(methods) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, method := range methods {
		if seen[method] {
			return false
		}
		seen[method] = true
	}
	return seen["pwd"] && (seen["otp"] || seen["email"] || seen["recovery"])
}

// RequireSudoTransaction checks canonical credential evidence while the caller
// holds the account lock. Refresh time, caller headers and cached session flags
// never constitute re-authentication. Elevation is separate from ordinary login
// assurance and requires a completed session-bound two-factor sudo challenge.
func (v *Verifier) RequireSudoTransaction(ctx context.Context, p Principal, tx Transaction) (Principal, error) {
	fresh, err := v.AuthenticateTransaction(ctx, p, tx)
	if err != nil {
		return Principal{}, err
	}
	if !sudoFirstPartySession(fresh.Session) {
		return Principal{}, ErrSudoRequired
	}
	reader, ok := tx.(SudoElevationReader)
	if !ok {
		return Principal{}, ErrSudoUnavailable
	}
	assurance, err := reader.SudoElevation(fresh.Session.ID)
	if errors.Is(err, ErrNotFound) {
		return Principal{}, ErrSudoRequired
	}
	if err != nil {
		return Principal{}, ErrSudoUnavailable
	}
	if !sudoAssurance(fresh, assurance, v.service.cfg.Now().UTC()) {
		return Principal{}, ErrSudoRequired
	}
	return fresh, nil
}

func sudoFirstPartySession(session Session) bool {
	return session.Type == 0 && session.ParentID == "" && session.ClientID == "" && session.Category == "device"
}

func sudoAssurance(p Principal, a SudoElevation, now time.Time) bool {
	return a.TrustPoints >= 2 && a.SessionID == p.Session.ID && a.AccountVersion == p.Account.Version && a.SessionEpoch == p.Session.Epoch && ValidSudoMethods(a.Methods) && !now.Before(a.AuthTime) && now.Sub(a.AuthTime) < 5*time.Minute && now.Before(a.ExpiresAt)
}
