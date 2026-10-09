// SPDX-License-Identifier: AGPL-3.0-only
// Authored Solian consent transport; owned OAuth/OIDC execution policy.
package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"sort"
	"strings"
	"time"
)

type Failure struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
	Status      int    `json:"-"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrRequest     = &Failure{"invalid_request", "Invalid authorization request.", 400}
	ErrClient      = &Failure{"invalid_client", "Client authentication failed.", 401}
	ErrGrant       = &Failure{"invalid_grant", "The authorization grant is invalid or expired.", 400}
	ErrScope       = &Failure{"invalid_scope", "The requested scope is not enabled.", 400}
	ErrDenied      = &Failure{"access_denied", "Current user authorization is required.", 403}
	ErrUnavailable = &Failure{"temporarily_unavailable", "Authorization is temporarily unavailable.", 503}
)

type Request struct {
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	ResponseType        string `json:"response_type"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Nonce               string `json:"nonce"`
}
type ClientInfo struct {
	ClientName  string    `json:"client_name"`
	ClientID    string    `json:"client_id"`
	HomeURI     *string   `json:"home_uri"`
	Description *string   `json:"description"`
	Scopes      []string  `json:"scopes"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type Decision struct {
	RedirectURI string `json:"redirect_uri"`
}
type TokenRequest struct{ GrantType, Code, RedirectURI, CodeVerifier, RefreshToken, ClientID, ClientSecret, DeviceCode string }
type Tokens struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	RefreshExpiresIn int64  `json:"refresh_expires_in,omitempty"`
	IDToken          string `json:"id_token"`
	Scope            string `json:"scope"`
}
type Repository interface {
	Preview(context.Context, string, Request) (ClientInfo, error)
	Decide(context.Context, string, Request, bool) (Decision, error)
	Token(context.Context, TokenRequest) (Tokens, error)
	UserInfo(context.Context, string) (map[string]any, error)
	RevokeConsent(context.Context, string, string) error
	RevokeToken(context.Context, string, string, string) error
}
type Verifier interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticateOAuth(context.Context, string) (authority.Principal, error)
}
type Metadata interface {
	OAuthIssuer() string
	OAuthJWKS() map[string]any
}
type Service struct {
	// Deployed consent UI calling this JSON/form API; required for discovery.
	AuthorizationEndpoint string
	Repository            Repository
	Verifier              Verifier
	Metadata              Metadata
	Bind                  func(context.Context, authority.Principal) context.Context
}

func Scopes(raw string) ([]string, error) {
	if raw == "" || len(raw) > 256 || strings.ContainsAny(raw, "\t\r\n") {
		return nil, ErrScope
	}
	parts := strings.Split(raw, " ")
	seen := map[string]bool{}
	for _, s := range parts {
		if !authority.OAuthScope(s) || seen[s] {
			return nil, ErrScope
		}
		seen[s] = true
	}
	if !seen["openid"] || len(parts) > 32 {
		return nil, ErrScope
	}
	sort.Strings(parts)
	return parts, nil
}
func Contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func Opaque(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func Redirect(s string) bool {
	u, e := url.Parse(s)
	if e != nil || len(s) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "[::1]" || u.Hostname() == "::1")) {
		return false
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return false
	}
	for k := range q {
		if k == "code" || k == "state" || k == "error" || k == "iss" {
			return false
		}
	}
	return true
}
func Validate(r Request, a apps.App) ([]string, error) {
	if r.ClientID != a.Slug || r.ResponseType != "code" || r.CodeChallengeMethod != "S256" || !Opaque(r.State, 16, 256) || r.Nonce != "" && !Opaque(r.Nonce, 16, 256) || !Redirect(r.RedirectURI) {
		return nil, ErrRequest
	}
	b, e := base64.RawURLEncoding.DecodeString(r.CodeChallenge)
	if e != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != r.CodeChallenge {
		return nil, ErrRequest
	}
	c := a.OAuthConfig
	if c == nil || !c.RequirePKCE || !Contains(c.AllowedGrantTypes, "authorization_code") || !Contains(c.RedirectURIs, r.RedirectURI) || len(c.RedirectURIs) > 32 {
		return nil, ErrClient
	}
	for _, g := range c.AllowedGrantTypes {
		if g != "authorization_code" && g != "refresh_token" && g != DeviceGrant {
			return nil, ErrClient
		}
	}
	s, e := Scopes(r.Scope)
	if e != nil {
		return nil, e
	}
	for _, scope := range s {
		if !Contains(c.AllowedScopes, scope) {
			return nil, ErrScope
		}
	}
	if Contains(s, "offline_access") && (!c.AllowOfflineAccess || !Contains(c.AllowedGrantTypes, "refresh_token")) {
		return nil, ErrScope
	}
	return s, nil
}
func PKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, r := range verifier {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:]) == challenge
}
