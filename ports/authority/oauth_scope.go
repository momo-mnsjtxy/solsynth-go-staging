// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"sort"
	"strings"
)

// The resource set is deliberately closed. OAuth wildcards never represent
// administrative, credential-management, or arbitrary future permissions.
var oauthResourceScopes = [...]string{
	"posts.view", "posts.create", "posts.update", "posts.delete",
	"chat.view", "chat.messages.create", "chat.messages.update", "chat.messages.delete",
	"profile.read", "wallet.read", "notifications.read", "realms.read",
}

var oauthIdentityScopes = [...]string{"openid", "profile", "email", "offline_access"}

// OAuthScopes lists the scopes advertised by discovery. Identity disclosure and
// offline access remain explicit grants; a resource wildcard cannot enable them.
func OAuthScopes() []string {
	out := append([]string{}, oauthIdentityScopes[:]...)
	out = append(out, oauthResourceScopes[:]...)
	return append(out, "*", "posts.*", "chat.*", "chat.messages.*", "profile.*", "wallet.*", "notifications.*", "realms.*")
}

// OAuthScope validates an exact known scope or a wildcard with a nonempty match
// inside the resource set. The signing path expands patterns before persistence.
func OAuthScope(scope string) bool {
	for _, known := range oauthIdentityScopes {
		if known == scope {
			return true
		}
	}
	if !validPattern(scope) {
		return false
	}
	for _, resource := range oauthResourceScopes {
		if matches(scope, resource) {
			return true
		}
	}
	return false
}

// ExpandOAuthScopes records the concrete consent in codes, grants and signed
// sessions. In particular, '*' can never reach the ordinary permission matcher
// and authorize a credential/admin operation below a delegated resource route.
func ExpandOAuthScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 || len(scopes) > 32 {
		return nil, ErrInvalid
	}
	seenInput := map[string]bool{}
	resolved := map[string]bool{}
	for _, scope := range scopes {
		if !OAuthScope(scope) || seenInput[scope] {
			return nil, ErrInvalid
		}
		seenInput[scope] = true
		if !strings.Contains(scope, "*") {
			resolved[scope] = true
			continue
		}
		for _, resource := range oauthResourceScopes {
			if matches(scope, resource) {
				resolved[resource] = true
			}
		}
	}
	out := make([]string, 0, len(resolved))
	for scope := range resolved {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out, nil
}

// OAuthScopeCovers compares configured scope patterns with concrete requests.
// Wildcards cover resource grants only, never OIDC identity/offline disclosure.
func OAuthScopeCovers(allowed, required string) bool {
	if !OAuthScope(allowed) || !OAuthScope(required) || strings.Contains(required, "*") {
		return false
	}
	if allowed == required {
		return true
	}
	for _, resource := range oauthResourceScopes {
		if resource == required {
			return matches(allowed, required)
		}
	}
	return false
}

// OAuthResourceScope applies OAuth wildcards only after the transport has chosen
// an explicitly delegable route. It grants no account permission: the resource
// operation must still check canonical grants, denies and ownership.
func OAuthResourceScope(session Session, required string) bool {
	if session.Type != 1 || session.Category != OAuthSessionCategory || !validPattern(required) || strings.Contains(required, "*") {
		return false
	}
	resource := false
	for _, known := range oauthResourceScopes {
		resource = resource || required == known
	}
	if !resource {
		return false
	}
	for _, scope := range session.Scopes {
		if OAuthScopeCovers(scope, required) {
			return true
		}
	}
	return false
}
