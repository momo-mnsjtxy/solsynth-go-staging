// SPDX-License-Identifier: AGPL-3.0-only
package connections

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/history"
	"strings"
	"unicode/utf8"
)

const Permission = "account.connections.manage"

var ErrInvalid = &authority.Failure{Code: "CONNECTION_INVALID", Message: "Invalid provider connection request.", Status: 400}
var ErrProof = &authority.Failure{Code: "CONNECTION_PROOF_INVALID", Message: "Provider proof is invalid, consumed, or expired.", Status: 403}
var ErrUnavailable = &authority.Failure{Code: "CONNECTION_PROVIDER_UNAVAILABLE", Message: "Provider linking is unavailable.", Status: 503}
var ErrConflict = &authority.Failure{Code: "CONNECTION_ALREADY_LINKED", Message: "Provider identity is already linked.", Status: 409}

type Started struct {
	State            string `json:"state"`
	AuthorizationURL string `json:"authorization_url"`
}
type Completion struct {
	State  string            `json:"state"`
	Code   string            `json:"code,omitempty"`
	OpenID map[string]string `json:"openid,omitempty"`
}
type Verified struct{ Identifier, Email, Username, DisplayName string }
type Provider interface {
	Name() string
	Start(state, challenge string) (string, error)
	Verify(context.Context, Completion, string) (Verified, error)
}
type Repository interface {
	Start(context.Context, string, string) (Started, error)
	Complete(context.Context, string, string, Completion) (history.Connection, error)
	Delete(context.Context, string, string) error
	SetPublic(context.Context, string, string, bool) (history.Connection, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}

func ValidProvider(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func ValidateVerified(v Verified) error {
	for _, x := range []struct {
		s string
		n int
	}{{v.Identifier, 2048}, {v.Email, 320}, {v.Username, 256}, {v.DisplayName, 256}} {
		if len(x.s) > x.n || strings.ContainsRune(x.s, 0) || !utf8.ValidString(x.s) {
			return ErrProof
		}
	}
	if v.Identifier == "" {
		return ErrProof
	}
	return nil
}
