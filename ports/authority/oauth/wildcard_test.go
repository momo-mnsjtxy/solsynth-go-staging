// SPDX-License-Identifier: AGPL-3.0-only
package oauth

import (
	"solsynth.local/go-migration/ports/authority"
	"testing"
)

func TestWildcardRegistrationAndConsentUseConcreteClosedScopes(t *testing.T) {
	r := Registration{Developer: "example", ProjectID: "a1100000-0000-4000-8000-000000000001", ClientName: "Example", RedirectURIs: []string{"https://example.invalid/callback"}, GrantTypes: []string{"authorization_code"}, ResponseTypes: []string{"code"}, Scope: "openid posts.*", TokenEndpointAuthMethod: "none"}
	scopes, err := ValidateRegistration(r)
	if err != nil || len(scopes) != 5 || !Contains(scopes, "posts.create") || Contains(scopes, "posts.*") {
		t.Fatal(scopes, err)
	}
	request, app, _ := validRequest()
	app.OAuthConfig.AllowedScopes = []string{"openid", "posts.*"}
	request.Scope = "openid posts.*"
	resolved, err := Validate(request, app)
	if err != nil || len(resolved) != 5 {
		t.Fatal(resolved, err)
	}
	request.Scope = "openid posts.view"
	if _, err = Validate(request, app); err != nil {
		t.Fatal("configured wildcard did not cover concrete request", err)
	}
	for _, raw := range []string{"openid *", "openid admin.*", "openid email", "openid offline_access", "openid chat.*"} {
		request.Scope = raw
		if _, err = Validate(request, app); err == nil {
			t.Fatal("consent exceeded configured resource/identity grant", raw)
		}
	}
	for _, advertised := range authority.OAuthScopes() {
		if !authority.OAuthScope(advertised) {
			t.Fatal("discovery advertised unissuable scope", advertised)
		}
	}
}
