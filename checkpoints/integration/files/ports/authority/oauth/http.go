// SPDX-License-Identifier: AGPL-3.0-only
package oauth

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

func RouteMatches(p string) bool {
	p = strings.TrimPrefix(p, "/stargate")
	return p == "/.well-known/openid-configuration" || p == "/auth/open/authorize" || p == "/auth/open/token" || p == "/auth/open/userinfo" || p == "/auth/open/jwks" || p == "/auth/open/revoke" || p == "/auth/open/consent" || p == "/auth/open/device" || p == "/auth/open/device/verify" || p == "/auth/open/introspect" || p == "/auth/open/register"
}
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func fail(w http.ResponseWriter, e error) {
	if errors.Is(e, authority.ErrUnavailable) {
		respond(w, 503, ErrUnavailable)
		return
	}
	var f *Failure
	if errors.As(e, &f) {
		if f.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Basic realm="oauth"`)
		}
		respond(w, f.Status, f)
		return
	}
	var a *authority.Failure
	if errors.As(e, &a) {
		status := a.Status
		if status == 423 {
			status = 403
		}
		respond(w, status, &Failure{Code: "access_denied", Description: "Current user authorization is required.", Status: status})
		return
	}
	respond(w, 503, ErrUnavailable)
}
func userFailure(w http.ResponseWriter, e error) {
	var a *authority.Failure
	if errors.Is(e, ErrGrant) || (errors.As(e, &a) && a.Status != 503) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		respond(w, 401, &Failure{Code: "invalid_token", Description: "The access token is invalid or expired.", Status: 401})
		return
	}
	fail(w, e)
}
func parameters(r *http.Request, keys ...string) (url.Values, error) {
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	var values url.Values
	var e error
	if r.Method == "GET" {
		values, e = url.ParseQuery(r.URL.RawQuery)
	} else {
		if r.URL.RawQuery != "" {
			return nil, ErrRequest
		}
		m, _, x := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if x != nil || m != "application/x-www-form-urlencoded" {
			return nil, ErrRequest
		}
		raw, x := io.ReadAll(r.Body)
		if x != nil {
			return nil, ErrRequest
		}
		values, e = url.ParseQuery(string(raw))
	}
	if e != nil {
		return nil, ErrRequest
	}
	for k, v := range values {
		if !allowed[k] || len(v) != 1 || len(v[0]) > 4096 {
			return nil, ErrRequest
		}
	}
	return values, nil
}
func request(v url.Values) Request {
	return Request{ClientID: v.Get("client_id"), RedirectURI: v.Get("redirect_uri"), ResponseType: v.Get("response_type"), Scope: v.Get("scope"), State: v.Get("state"), CodeChallenge: v.Get("code_challenge"), CodeChallengeMethod: v.Get("code_challenge_method"), Nonce: v.Get("nonce")}
}
func credentials(r *http.Request, v url.Values) (client, secret string, e error) {
	client, secret = v.Get("client_id"), v.Get("client_secret")
	headers := r.Header.Values("Authorization")
	if len(headers) > 1 {
		return "", "", ErrClient
	}
	if len(headers) == 1 {
		if secret != "" {
			return "", "", ErrClient
		}
		username, password, ok := r.BasicAuth()
		if !ok {
			return "", "", ErrClient
		}
		decoded, x := url.QueryUnescape(username)
		raw, x2 := url.QueryUnescape(password)
		if x != nil || x2 != nil || client != "" && client != decoded {
			return "", "", ErrClient
		}
		client, secret = decoded, raw
	}
	if len(client) < 1 || len(client) > 128 || len(secret) > 256 {
		return "", "", ErrClient
	}
	return
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil || s.Verifier == nil || s.Metadata == nil || s.Bind == nil {
		fail(w, ErrUnavailable)
		return
	}
	if !RouteMatches(r.URL.Path) {
		w.WriteHeader(404)
		return
	}
	if r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.User != nil {
		fail(w, ErrRequest)
		return
	}
	for h := range r.Header {
		k := strings.ToLower(h)
		if k == "cookie" || k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-app-secret" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			fail(w, ErrRequest)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	p := strings.TrimPrefix(r.URL.Path, "/stargate")
	if s.serveExtension(w, r, p) {
		return
	}
	if p == "/.well-known/openid-configuration" || p == "/auth/open/jwks" {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(405)
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, ErrRequest)
			return
		}
		if p == "/auth/open/jwks" {
			respond(w, 200, s.Metadata.OAuthJWKS())
			return
		}
		issuer := strings.TrimSuffix(s.Metadata.OAuthIssuer(), "/")
		if u, e := url.Parse(s.AuthorizationEndpoint); e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			fail(w, ErrUnavailable)
			return
		}
		respond(w, 200, map[string]any{"issuer": s.Metadata.OAuthIssuer(), "authorization_endpoint": s.AuthorizationEndpoint, "token_endpoint": issuer + "/auth/open/token", "userinfo_endpoint": issuer + "/auth/open/userinfo", "jwks_uri": issuer + "/auth/open/jwks", "revocation_endpoint": issuer + "/auth/open/revoke", "response_types_supported": []string{"code"}, "device_authorization_endpoint": issuer + "/auth/open/device", "introspection_endpoint": issuer + "/auth/open/introspect", "registration_endpoint": issuer + "/auth/open/register", "introspection_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"}, "grant_types_supported": []string{"authorization_code", "refresh_token", DeviceGrant}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"openid", "profile", "email", "offline_access", "posts.view", "posts.create", "posts.update", "posts.delete", "chat.view", "chat.messages.create", "chat.messages.update", "chat.messages.delete", "profile.read", "wallet.read", "notifications.read", "realms.read"}, "claims_supported": []string{"iss", "aud", "sub", "sid", "iat", "exp", "auth_time", "nonce", "name", "preferred_username", "locale", "email", "email_verified"}, "authorization_response_iss_parameter_supported": true})
		return
	}
	if p == "/auth/open/token" || p == "/auth/open/revoke" {
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return
		}
		keys := []string{"grant_type", "code", "redirect_uri", "code_verifier", "refresh_token", "client_id", "client_secret", "device_code"}
		if p == "/auth/open/revoke" {
			keys = []string{"token", "token_type_hint", "client_id", "client_secret"}
		}
		v, e := parameters(r, keys...)
		if e != nil {
			fail(w, e)
			return
		}
		client, secret, e := credentials(r, v)
		if e != nil {
			fail(w, e)
			return
		}
		if p == "/auth/open/revoke" {
			if hint := v.Get("token_type_hint"); hint != "" && hint != "refresh_token" {
				fail(w, &Failure{Code: "unsupported_token_type", Description: "This endpoint revokes refresh token families.", Status: 400})
				return
			}
			if e = s.Repository.RevokeToken(r.Context(), client, secret, v.Get("token")); e != nil {
				fail(w, e)
				return
			}
			respond(w, 200, nil)
			return
		}
		out, e := s.Repository.Token(r.Context(), TokenRequest{GrantType: v.Get("grant_type"), Code: v.Get("code"), RedirectURI: v.Get("redirect_uri"), CodeVerifier: v.Get("code_verifier"), RefreshToken: v.Get("refresh_token"), ClientID: client, ClientSecret: secret, DeviceCode: v.Get("device_code")})
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, out)
		return
	}
	if p == "/auth/open/userinfo" {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(405)
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, ErrRequest)
			return
		}
		token, e := authority.BearerToken(r)
		if e != nil {
			userFailure(w, e)
			return
		}
		proof, e := s.Verifier.AuthenticateOAuth(r.Context(), token)
		if e != nil {
			userFailure(w, e)
			return
		}
		out, e := s.Repository.UserInfo(s.Bind(r.Context(), proof), proof.Account.ID)
		if e != nil {
			userFailure(w, e)
			return
		}
		respond(w, 200, out)
		return
	}
	proof, e := s.Verifier.AuthenticateRequest(r)
	if e != nil {
		fail(w, e)
		return
	}
	if proof.Session.Type != 0 {
		fail(w, ErrDenied)
		return
	}
	ctx := s.Bind(r.Context(), proof)
	if p == "/auth/open/consent" {
		if r.Method != "DELETE" {
			w.Header().Set("Allow", "DELETE")
			w.WriteHeader(405)
			return
		}
		v, e := parameters(r, "client_id")
		if e != nil {
			fail(w, e)
			return
		}
		if e = s.Repository.RevokeConsent(ctx, proof.Account.ID, v.Get("client_id")); e != nil {
			fail(w, e)
			return
		}
		respond(w, 204, nil)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
		return
	}
	keys := []string{"client_id", "redirect_uri", "response_type", "scope", "state", "code_challenge", "code_challenge_method", "nonce"}
	if r.Method == "POST" {
		keys = append(keys, "authorize")
	}
	v, e := parameters(r, keys...)
	if e != nil {
		fail(w, e)
		return
	}
	if r.Method == "GET" {
		out, e := s.Repository.Preview(ctx, proof.Account.ID, request(v))
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, out)
		return
	}
	if v.Get("authorize") != "true" && v.Get("authorize") != "false" {
		fail(w, ErrRequest)
		return
	}
	out, e := s.Repository.Decide(ctx, proof.Account.ID, request(v), v.Get("authorize") == "true")
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, out)
}
