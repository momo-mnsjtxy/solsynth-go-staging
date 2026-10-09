// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

// Resource delegation is an explicit route/method allowlist. Unlisted routes
// (especially account mutations, administration, app secrets and wallet writes)
// never promote an OAuth token into the ordinary user credential lane.
func oauthResourceScope(r *http.Request) string {
	if r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") {
		return ""
	}
	p := strings.TrimPrefix(r.URL.Path, "/sphere")
	p = strings.TrimPrefix(p, "/passport")
	p = strings.TrimPrefix(p, "/messager")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 || parts[0] != "api" {
		return ""
	}
	if r.Method == "GET" {
		switch parts[1] {
		case "posts":
			return "posts.view"
		case "chat":
			return "chat.view"
		case "accounts":
			if len(parts) == 3 && (parts[2] == "me" || authority.ValidID(parts[2])) {
				return "profile.read"
			}
		case "wallets":
			return "wallet.read"
		case "notifications":
			return "notifications.read"
		case "realms":
			return "realms.read"
		}
	}
	if parts[1] == "posts" {
		if r.Method == "POST" && len(parts) == 2 {
			return "posts.create"
		}
		if len(parts) == 3 && authority.ValidID(parts[2]) {
			if r.Method == "PATCH" || r.Method == "PUT" {
				return "posts.update"
			}
			if r.Method == "DELETE" {
				return "posts.delete"
			}
		}
	}
	if parts[1] == "chat" && len(parts) >= 4 && authority.ValidID(parts[2]) && parts[3] == "messages" {
		if r.Method == "POST" && len(parts) == 4 {
			return "chat.messages.create"
		}
		if len(parts) == 5 && authority.ValidID(parts[4]) {
			if r.Method == "PATCH" || r.Method == "PUT" {
				return "chat.messages.update"
			}
			if r.Method == "DELETE" {
				return "chat.messages.delete"
			}
		}
	}
	return ""
}
func authenticateResource(v *authority.Verifier, r *http.Request) (authority.Principal, error) {
	scope := oauthResourceScope(r)
	if scope == "" {
		return authority.Principal{}, authority.ErrInvalid
	}
	token, e := authority.BearerToken(r)
	if e != nil {
		return authority.Principal{}, e
	}
	p, e := v.AuthenticateOAuth(r.Context(), token)
	if e != nil {
		return p, e
	}
	for _, s := range p.Session.Scopes {
		if s == scope {
			return p, nil
		}
	}
	return authority.Principal{}, authority.ErrForbidden
}
