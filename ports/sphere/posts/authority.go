// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"context"
	"net/http"
	"strings"

	"solsynth.local/go-migration/ports/authority"
)

type principalKey struct{}
type verifiedAuth struct{ verifier *authority.Verifier }

func (a verifiedAuth) Authenticate(r *http.Request) (string, error) {
	p, ok := r.Context().Value(principalKey{}).(authority.Principal)
	if !ok {
		return "", nil
	}
	p, e := a.verifier.AuthenticatePrincipal(r.Context(), p)
	if e != nil {
		return "", e
	}
	return p.Account.ID, nil
}
func (a verifiedAuth) Authorize(r *http.Request, permission string) error {
	p, ok := r.Context().Value(principalKey{}).(authority.Principal)
	if !ok {
		return ErrUnauthenticated
	}
	_, e := a.verifier.AuthorizePrincipal(r.Context(), p, permission)
	return e
}

// NewVerifiedHandler is the standalone service's verification boundary. The
// unified host can supply its own private-proof adapter to NewHandler instead.
func NewVerifiedHandler(repo Repository, v *authority.Verifier) http.Handler {
	next := NewHandler(repo, verifiedAuth{v})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			failure(w, ErrUnavailable)
			return
		}
		for k := range r.Header {
			lower := strings.ToLower(k)
			if lower == "x-account-id" || lower == "x-user-id" || lower == "x-app-secret" || lower == "proxy-authorization" || strings.HasPrefix(lower, "x-test-") || strings.HasPrefix(lower, "x-synthetic-") {
				failure(w, ErrForbidden)
				return
			}
		}
		for k := range r.URL.Query() {
			if k == "tk" || k == "token" || k == "access_token" {
				failure(w, ErrInvalid)
				return
			}
		}
		if r.URL.User != nil {
			failure(w, ErrInvalid)
			return
		}
		copy := r.Clone(r.Context())
		present := false
		for k := range r.Header {
			if strings.EqualFold(k, "Authorization") {
				present = true
			}
		}
		if present {
			p, e := v.AuthenticateRequest(r)
			if e != nil {
				failure(w, e)
				return
			}
			copy = copy.WithContext(context.WithValue(copy.Context(), principalKey{}, p))
		}
		copy.Header.Del("Authorization")
		copy.Header.Del("Cookie")
		next.ServeHTTP(w, copy)
	})
}
