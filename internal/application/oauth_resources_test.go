// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http/httptest"
	"testing"
)

func TestOAuthResourceClosedRouteMapping(t *testing.T) {
	for _, tc := range []struct{ method, path, scope string }{
		{"GET", "/stargate/accounts/me", "profile.read"}, {"DELETE", "/stargate/accounts/me", ""}, {"POST", "/stargate/accounts/me", ""}, {"GET", "/stargate/accounts/me/password", ""}, {"GET", "/stargate/accounts/../accounts/me", ""}, {"GET", "/stargate/accounts/%6de", ""},
		{"GET", "/stargate/wallets", "wallet.read"}, {"GET", "/stargate/notifications", "notifications.read"}, {"GET", "/stargate/realms", "realms.read"}, {"GET", "/stargate/admin/payments/orders", ""}, {"POST", "/stargate/wallets/transfer", ""},
		{"GET", "/api/posts", "posts.view"}, {"GET", "/api/accounts/me", "profile.read"}, {"GET", "/api/chat/unread", "chat.view"}, {"GET", "/api/wallets", "wallet.read"}, {"POST", "/api/posts", "posts.create"},
		{"GET", "/api/admin/accounts", ""}, {"DELETE", "/api/accounts/me", ""}, {"POST", "/api/wallets/transfer", ""}, {"POST", "/api/private/apps", ""}, {"GET", "/api/posts/../admin/accounts", ""}, {"POST", "/api/chat/create", ""}, {"GET", "/api/posts%2Fadmin", ""},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := oauthResourceScope(r); got != tc.scope {
			t.Errorf("%s %s: %q != %q", tc.method, tc.path, got, tc.scope)
		}
	}
}
