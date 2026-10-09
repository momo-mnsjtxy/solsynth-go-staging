// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/passport/board"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

// Private keys prevent caller headers, JSON and unrelated middleware from
// constructing a trusted identity. Only bindIdentity stores the verified proof.
type principalKey struct{}
type identity struct{ verifier *authority.Verifier }

func (i identity) require(ctx context.Context, permissions ...string) (authority.Principal, error) {
	p, ok := ctx.Value(principalKey{}).(authority.Principal)
	if !ok {
		return authority.Principal{}, authority.ErrInvalid
	}
	if i.verifier == nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	if len(permissions) == 0 {
		return i.verifier.AuthenticatePrincipal(ctx, p)
	}
	return i.verifier.AuthorizePrincipal(ctx, p, permissions...)
}
func bindIdentity(v *authority.Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Alternate identity channels are unsupported and never silently promoted.
		authPresent := false
		for name := range r.Header {
			if strings.EqualFold(name, "Authorization") {
				authPresent = true
			}
			k := strings.ToLower(name)
			if strings.HasPrefix(k, "x-synthetic-") || strings.HasPrefix(k, "x-test-") || k == "x-account-id" || k == "x-user-id" || k == "x-app-secret" || k == "proxy-authorization" {
				problem(w, 403, "IDENTITY_CHANNEL_UNSUPPORTED")
				return
			}
		}
		for k := range r.URL.Query() {
			if k == "access_token" || k == "token" || k == "tk" {
				problem(w, 400, "QUERY_CREDENTIAL_UNSUPPORTED")
				return
			}
		}
		if r.URL.User != nil {
			problem(w, 400, "URL_CREDENTIAL_UNSUPPORTED")
			return
		}
		copy := r.Clone(r.Context())
		if authPresent {
			if v == nil {
				writeAuthorityError(w, authority.ErrUnavailable)
				return
			}
			p, err := v.AuthenticateRequest(r)
			if err != nil && errors.Is(err, authority.ErrInvalid) {
				p, err = authenticateResource(v, r)
			}
			if err != nil {
				writeAuthorityError(w, err)
				return
			}
			copy = copy.WithContext(context.WithValue(copy.Context(), principalKey{}, p))
		}
		// Downstream adapters retain only verified proof. Never put bearer bytes in
		// domain principal structs, context, cache, logs or a long-lived map.
		copy.Header.Del("Authorization")
		copy.Header.Del("Cookie")
		next.ServeHTTP(w, copy)
	})
}
func problem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="backend"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}
func writeAuthorityError(w http.ResponseWriter, err error) {
	var f *authority.Failure
	if errors.As(err, &f) {
		problem(w, f.Status, f.Code)
		return
	}
	problem(w, 503, "AUTHORITY_UNAVAILABLE")
}
func boardError(err error) error {
	var f *authority.Failure
	if errors.As(err, &f) {
		if f.Status == 401 {
			return board.ErrUnauthenticated
		}
		if f.Status == 403 || f.Status == 423 {
			return board.ErrForbidden
		}
	}
	return &board.DependencyError{Adapter: "owned authority"}
}
func taxonomyError(err error) error {
	var f *authority.Failure
	if errors.As(err, &f) {
		if f.Status == 401 {
			return taxonomy.ErrUnauthenticated
		}
		if f.Status == 403 || f.Status == 423 {
			return taxonomy.ErrForbidden
		}
	}
	return taxonomy.ErrPolicyUnverified
}
func messagerError(err error) error {
	var f *authority.Failure
	if errors.As(err, &f) {
		if f.Status == 401 {
			return messager.ErrSyncUnauthorized
		}
		if f.Status == 403 || f.Status == 423 {
			return messager.ErrSyncForbidden
		}
	}
	return messager.ErrSyncUnavailable
}

type boardIdentity struct{ identity }

func (a boardIdentity) Authenticate(r *http.Request) (board.Principal, error) {
	p, ok := r.Context().Value(principalKey{}).(authority.Principal)
	if !ok {
		return board.Principal{}, board.ErrUnauthenticated
	}
	return board.Principal{AccountID: p.Account.ID, Name: p.Account.Name}, nil
}
func (a boardIdentity) Require(ctx context.Context, p board.Principal, permission string) error {
	verified, err := a.require(ctx, permission)
	if err != nil {
		return boardError(err)
	}
	if p.AccountID != verified.Account.ID || p.Name != verified.Account.Name || p.AppID != "" {
		return board.ErrForbidden
	}
	return nil
}

// Public Passport board reads are explicitly declared in the retained public
// account controller. App-secret/consent identity remains unsupported.
func (a boardIdentity) PublicRead(ctx context.Context) error { return ctx.Err() }
func (a boardIdentity) AuthorizeApp(context.Context, board.Principal, string, string) error {
	return board.ErrForbidden
}

type syncIdentity struct{ identity }

func (a syncIdentity) Account(r *http.Request) (string, error) {
	_, ok := r.Context().Value(principalKey{}).(authority.Principal)
	if !ok && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/messages") {
		return "", nil
	}
	p, err := a.require(r.Context())
	if err != nil {
		return "", messagerError(err)
	}
	return p.Account.ID, nil
}
