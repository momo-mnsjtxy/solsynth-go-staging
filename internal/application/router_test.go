// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayAndDirectRouting(t *testing.T) {
	cases := []struct{ path, module, canonical string }{
		{"/passport/accounts/me/name-change-card/order", "namecards", "/api/accounts/me/name-change-card/order"},
		{"/stargate/accounts/me/name-change-card/use", "namecards", "/stargate/accounts/me/name-change-card/use"},
		{"/api/accounts/me/name-change-card", "namecards", "/api/accounts/me/name-change-card"},
		{"/passport/rewind/me", "rewind", "/api/rewind/me"},
		{"/stargate/rewind/me/2026/public", "rewind", "/stargate/rewind/me/2026/public"},
		{"/api/rewind/synthetic", "rewind", "/api/rewind/synthetic"},
		{"/passport/accounts/me/board", "board", "/api/accounts/me/board"},
		{"/passport/admin/accounts/a/board", "board", "/api/admin/accounts/a/board"},
		{"/api/accounts/a/board", "board", "/api/accounts/a/board"},
		{"/sphere/posts/tags", "taxonomy", "/api/posts/tags"},
		{"/sphere/admin/posts/tags/test/protect", "taxonomy", "/api/admin/posts/tags/test/protect"},
		{"/api/posts/categories", "taxonomy", "/api/posts/categories"},
		{"/sphere/posts", "posts", "/api/posts"},
		{"/sphere/posts/drafts", "posts", "/api/posts/drafts"},
		{"/api/posts/10000000-0000-4000-8000-000000000001", "posts", "/api/posts/10000000-0000-4000-8000-000000000001"},
		{"/sphere/publishers/individual", "accounts", "/api/publishers/individual"},
		{"/passport/accounts/me", "accounts", "/api/accounts/me"},
		{"/stargate/accounts/me", "accounts", "/stargate/accounts/me"},
		{"/messager/chat/a/sync", "messager", "/api/chat/a/sync"},
		{"/api/chat/unread", "messager", "/api/chat/unread"},
		{"/wallet/wallets/transfer", "wallet", "/api/wallets/transfer"},
		{"/api/wallets/all", "wallet", "/api/wallets/all"},
		{"/stargate/accounts/me/pin-status", "wallet", "/api/accounts/me/pin-status"},
		{"/stargate/auth/token", "session", "/stargate/auth/token"},
		{"/api/auth/token", "session", "/stargate/auth/token"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			handler := func(module string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if module != tc.module || r.URL.Path != tc.canonical || r.URL.RawQuery != "take=7" || r.Method != "POST" {
						t.Fatalf("wrong routing %s %s %s", module, r.URL.Path, r.URL.RawQuery)
					}
					w.Header().Set("Location", "/api/posts/tags/test/subscription")
					w.WriteHeader(201)
				})
			}
			h := router{modules{namecards: handler("namecards"), rewind: handler("rewind"), board: handler("board"), taxonomy: handler("taxonomy"), posts: handler("posts"), messager: handler("messager"), wallet: handler("wallet"), accounts: handler("accounts"), session: handler("session")}}
			r := httptest.NewRequest("POST", tc.path+"?take=7", nil)
			before := r.URL.String()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 201 || r.URL.String() != before {
				t.Fatal(w.Code, r.URL.String())
			}
			if tc.module == "taxonomy" && tc.path[1] == 's' && w.Header().Get("Location") != "/sphere/posts/tags/test/subscription" {
				t.Fatal(w.Header())
			}
		})
	}
}
func TestRoutingRejectsCrossServiceAndAmbiguousPaths(t *testing.T) {
	h := router{modules{board: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not dispatch") }), taxonomy: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not dispatch") })}}
	for _, p := range []string{"/passport/posts/tags", "/sphere/accounts/a/board", "/unknown/chat/unread", "/sphere//posts/tags", "/sphere/../passport/accounts/a/board", "/sphere/posts%2ftags", "/sphere/posts/ta%67s"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 400 && w.Code != 404 {
			t.Fatal(p, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/develop/private/apps/10000000-0000-4000-8000-000000000001/board/payload", nil))
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("HEAD", "/healthz", nil))
	if w.Code != 405 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
