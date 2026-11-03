// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"errors"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"testing"
)

func TestDeviceWildcardUsesSameConcreteConsentAsAuthorizationCode(t *testing.T) {
	app := apps.App{OAuthConfig: &apps.OAuthConfig{AllowedScopes: []string{"openid", "posts.*"}, AllowedGrantTypes: []string{oauth.DeviceGrant}, IsPublicClient: true}}
	for _, raw := range []string{"openid posts.*", "openid posts.view posts.create"} {
		scopes, err := deviceScopes(raw, app)
		if err != nil || !oauth.Contains(scopes, "posts.create") || oauth.Contains(scopes, "posts.*") {
			t.Fatal(raw, scopes, err)
		}
	}
	for _, raw := range []string{"openid *", "openid chat.*", "openid email", "openid offline_access", "openid admin.*"} {
		if _, err := deviceScopes(raw, app); !errors.Is(err, oauth.ErrScope) {
			t.Fatal("device scope exceeded concrete app consent", raw, err)
		}
	}
}
