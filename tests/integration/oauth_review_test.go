// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"errors"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"testing"
)

func TestPGOAuthPreviewRechecksRegistrationAndOwnerVersion(t *testing.T) {
	for _, kind := range []string{"app_epoch", "owner_version", "owner_identity"} {
		t.Run(kind, func(t *testing.T) {
			f := newOAuthFixture(t, false)
			req, _ := oauthRequest(f)
			if _, e := f.repo.Preview(f.ctx, f.subject, req); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "app_epoch":
				registration := *f.app.OAuthConfig
				registration.RedirectURIs = append(append([]string{}, registration.RedirectURIs...), "https://synthetic-client.invalid/second-callback")
				backendCall(t, f.handler, "PATCH", "/api/private/apps/"+f.app.ID+f.query, f.token, devJSON(t, apps.AppPatch{OAuthConfig: &registration}), 200)
			case "owner_version":
				query(t, f.db, `UPDATE owned_authority_accounts SET version=version+1 WHERE id=$1::uuid`, f.actor)
			case "owner_identity":
				query(t, f.db, `UPDATE owned_publishers SET account_id=$2::uuid WHERE id=$1::uuid`, f.app.PublisherID, f.subject)
			}
			if _, e := f.repo.Preview(f.ctx, f.subject, req); !errors.Is(e, oauth.ErrGrant) {
				t.Fatal("stale preview returned success", kind, e)
			}
			if _, e := f.repo.Decide(f.ctx, f.subject, req, true); !errors.Is(e, oauth.ErrGrant) {
				t.Fatal("stale preview could be approved", kind, e)
			}
			fresh, _ := oauthRequest(f)
			if _, e := f.repo.Preview(f.ctx, f.subject, fresh); e != nil {
				t.Fatal("fresh preview unavailable", kind, e)
			}
		})
	}
}

func TestPGOAuthConfidentialPurposeScopeWithdrawal(t *testing.T) {
	for _, confidential := range []bool{true, false} {
		name := "public"
		if confidential {
			name = "confidential"
		}
		t.Run(name, func(t *testing.T) {
			f := newOAuthFixture(t, confidential)
			registration := *f.app.OAuthConfig
			registration.AllowedScopes = []string{"openid", "profile", "email", "offline_access"}
			backendCall(t, f.handler, "PATCH", "/api/private/apps/"+f.app.ID+f.query, f.token, devJSON(t, apps.AppPatch{OAuthConfig: &registration}), 200)
			req, verifier, code := oauthCode(t, f)
			tokens, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: "authorization_code", ClientID: req.ClientID, ClientSecret: f.secret, Code: code, CodeVerifier: verifier, RedirectURI: req.RedirectURI})
			if confidential {
				if !errors.Is(e, oauth.ErrClient) || tokens.AccessToken != "" {
					t.Fatal("withdrawn credential purpose accepted", e)
				}
			} else if e != nil || tokens.AccessToken == "" {
				t.Fatal("public client incorrectly requires confidential purpose", e)
			}
		})
	}
}
