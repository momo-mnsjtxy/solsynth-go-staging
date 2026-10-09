// SPDX-License-Identifier: AGPL-3.0-only
package oauth

import "testing"

func TestRegistrationClosedMetadataAndOwnershipCoordinates(t *testing.T) {
	base := Registration{Developer: "example", ProjectID: "a1100000-0000-4000-8000-000000000001", ClientName: "Example", RedirectURIs: []string{"https://example.invalid/callback"}, GrantTypes: []string{"authorization_code", DeviceGrant}, ResponseTypes: []string{"code"}, Scope: "openid posts.view profile.read", TokenEndpointAuthMethod: "none"}
	if scopes, e := ValidateRegistration(base); e != nil || len(scopes) != 3 {
		t.Fatal(scopes, e)
	}
	for _, mutate := range []func(*Registration){func(r *Registration) { r.Scope = "openid *" }, func(r *Registration) { r.GrantTypes = []string{"password"} }, func(r *Registration) { r.RedirectURIs = []string{"https://user:password@example.invalid/callback"} }, func(r *Registration) { r.ResponseTypes = []string{"token"} }, func(r *Registration) { r.TokenEndpointAuthMethod = "private_key_jwt" }, func(r *Registration) { r.ProjectID = "" }} {
		r := base
		mutate(&r)
		if _, e := ValidateRegistration(r); e == nil {
			t.Fatal("invalid registration accepted", r)
		}
	}
}
