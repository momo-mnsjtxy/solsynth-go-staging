// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http/httptest"
	"testing"
)

func TestOAuthResourceClosedRouteMapping(t *testing.T) {
	for _, tc := range []struct{ method, path, scope string }{
		{"GET", "/api/posts", "posts.view"}, {"GET", "/api/accounts/me", "profile.read"}, {"GET", "/api/chat/unread", "chat.view"}, {"GET", "/api/wallets", "wallet.read"}, {"POST", "/api/posts", "posts.create"},
		{"GET", "/api/admin/accounts", ""}, {"DELETE", "/api/accounts/me", ""}, {"POST", "/api/wallets/transfer", ""}, {"POST", "/api/private/apps", ""}, {"GET", "/api/posts/../admin/accounts", ""}, {"POST", "/api/chat/create", ""}, {"GET", "/api/posts%2Fadmin", ""},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := oauthResourceScope(r); got != tc.scope {
			t.Errorf("%s %s: %q != %q", tc.method, tc.path, got, tc.scope)
		}
	}
}
