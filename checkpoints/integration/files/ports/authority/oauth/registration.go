// SPDX-License-Identifier: AGPL-3.0-only
package oauth

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

// Registration is authenticated owner registration. Developer/project are
// canonical ownership coordinates, not caller-supplied grants or roles.
type Registration struct {
	Developer               string   `json:"developer"`
	ProjectID               string   `json:"project_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               *string  `json:"client_uri,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}
type RegistrationResult struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            *string  `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	ClientName              string   `json:"client_name"`
	ClientURI               *string  `json:"client_uri,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}
type RegistrationRepository interface {
	Register(context.Context, string, Registration) (RegistrationResult, error)
}

func ValidateRegistration(r Registration) ([]string, error) {
	if !authority.ValidID(r.ProjectID) || r.Developer == "" || len(r.Developer) > 64 || len(r.ClientName) == 0 || len(r.ClientName) > 200 || strings.TrimSpace(r.ClientName) != r.ClientName || strings.ContainsAny(r.ClientName, "\x00\r\n") {
		return nil, ErrRequest
	}
	if r.TokenEndpointAuthMethod != "none" && r.TokenEndpointAuthMethod != "client_secret_basic" && r.TokenEndpointAuthMethod != "client_secret_post" {
		return nil, ErrClient
	}
	if len(r.RedirectURIs) > 32 || len(r.GrantTypes) == 0 || len(r.GrantTypes) > 3 || len(r.ResponseTypes) != 1 || r.ResponseTypes[0] != "code" {
		return nil, ErrRequest
	}
	grants := map[string]bool{}
	for _, g := range r.GrantTypes {
		if grants[g] || g != "authorization_code" && g != "refresh_token" && g != DeviceGrant {
			return nil, ErrRequest
		}
		grants[g] = true
	}
	if !grants["authorization_code"] && !grants[DeviceGrant] {
		return nil, ErrRequest
	}
	if grants["authorization_code"] && len(r.RedirectURIs) == 0 {
		return nil, ErrRequest
	}
	seen := map[string]bool{}
	for _, uri := range r.RedirectURIs {
		if !Redirect(uri) || seen[uri] {
			return nil, ErrRequest
		}
		seen[uri] = true
	}
	if r.ClientURI != nil && !Redirect(*r.ClientURI) {
		return nil, ErrRequest
	}
	scopes, e := Scopes(r.Scope)
	if e != nil {
		return nil, e
	}
	if Contains(scopes, "offline_access") && !grants["refresh_token"] {
		return nil, ErrScope
	}
	return scopes, nil
}
