// SPDX-License-Identifier: AGPL-3.0-only
// 路由契约回归：以调用者看到的处理器、路径和响应验证跨业务覆盖规则。
// 调用示例：go test ./internal/application -run TestRouteFamilies -count=1。
package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func routeFamilyMarker(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Route-Family", name)
		w.Header().Set("Route-Path", r.URL.Path)
		w.WriteHeader(http.StatusAccepted)
	})
}

func TestRouteFamiliesPreserveHandlerOwnership(t *testing.T) {
	h := router{modules{
		presence: routeFamilyMarker("presence"), presenceSensors: routeFamilyMarker("sensors"),
		accounts: routeFamilyMarker("accounts"), board: routeFamilyMarker("board"),
		posts: routeFamilyMarker("posts"), taxonomy: routeFamilyMarker("taxonomy"),
		timeline: routeFamilyMarker("timeline"), interactions: routeFamilyMarker("interactions"),
		social: routeFamilyMarker("social"), messager: routeFamilyMarker("chat"),
		calls: routeFamilyMarker("calls"), wallet: routeFamilyMarker("wallet"),
		merchants: routeFamilyMarker("merchants"), commerce: routeFamilyMarker("commerce"),
		apps: routeFamilyMarker("apps"), tickets: routeFamilyMarker("tickets"),
	}}
	cases := []struct{ method, path, family, canonical string }{
		{"POST", "/passport/accounts/me/presence/sensors", "sensors", "/api/accounts/me/presence/sensors"},
		{"GET", "/sphere/posts", "timeline", "/api/posts"},
		{"POST", "/sphere/posts", "posts", "/api/posts"},
		{"HEAD", "/sphere/posts", "posts", "/api/posts"},
		{"GET", "/sphere/posts/id/replies", "timeline", "/api/posts/id/replies"},
		{"POST", "/sphere/posts/id/reactions", "interactions", "/api/posts/id/reactions"},
		{"GET", "/sphere/posts/tags", "taxonomy", "/api/posts/tags"},
		{"GET", "/sphere/publishers/alice/collections", "interactions", "/api/publishers/alice/collections"},
		{"GET", "/sphere/publishers/subscriptions", "timeline", "/api/publishers/subscriptions"},
		{"GET", "/passport/realms/room/members", "social", "/api/realms/room/members"},
		{"GET", "/passport/realms/room/chat", "social", "/api/realms/room/chat"},
		{"GET", "/api/realms/room/chat", "chat", "/api/realms/room/chat"},
		{"POST", "/messager/chat/realtime/call/join", "calls", "/api/chat/realtime/call/join"},
		{"GET", "/wallet/subscriptions/billing", "merchants", "/api/subscriptions/billing"},
		{"GET", "/wallet/subscriptions/plan", "commerce", "/api/subscriptions/plan"},
		{"GET", "/develop/private/apps/app/board/payload", "apps", "/api/private/apps/app/board/payload"},
		{"GET", "/passport/accounts/me/activation/progress", "tickets", "/api/tests/activation"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != http.StatusAccepted || w.Header().Get("Route-Family") != tc.family || w.Header().Get("Route-Path") != tc.canonical {
				t.Fatalf("status=%d headers=%v", w.Code, w.Header())
			}
		})
	}
}

func TestRouteFamiliesPreserveUnavailableAndFallbackPolicies(t *testing.T) {
	h := router{modules{
		accounts: routeFamilyMarker("accounts"), notifications: routeFamilyMarker("notifications"),
		social: routeFamilyMarker("social"), board: routeFamilyMarker("board"),
		apps: routeFamilyMarker("apps"),
	}}
	cases := []struct {
		path, family, problem string
		status                int
	}{
		{"/api/notifications/devices", "", "NOTIFICATION_UNAVAILABLE", 503},
		{"/api/accounts/me/presence/sensors", "", "ACTIVITIES_UNAVAILABLE", 503},
		{"/api/accounts/me/connections/provider", "", "CONNECTION_PROVIDER_UNAVAILABLE", 503},
		{"/passport/accounts/me/connections/provider", "accounts", "", 202},
		{"/stargate/accounts/me/connections/provider", "accounts", "", 202},
		{"/passport/accounts/me/connections", "", "ACCOUNT_HISTORY_UNAVAILABLE", 503},
		{"/api/realms/room/members?label_id=label", "", "REALM_MEMBER_UNAVAILABLE", 503},
		{"/api/realms/room/boosts", "", "REALM_BOOST_UNAVAILABLE", 503},
		{"/passport/private/apps/app/board/payload", "", "APP_IDENTITY_UNSUPPORTED", 501},
		{"/sphere/posts", "", "", 404},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.status || w.Header().Get("Route-Family") != tc.family || tc.problem != "" && !strings.Contains(w.Body.String(), tc.problem) {
				t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
		})
	}
}

func TestRouteFamiliesKeepRealmExperienceOwnership(t *testing.T) {
	for _, useMemberHistory := range []bool{false, true} {
		h := router{modules{
			realmMembers: routeFamilyMarker("members"), realmBoosts: routeFamilyMarker("boosts"),
			realmMemberHistory: useMemberHistory,
		}}
		for _, prefix := range []string{"/api", "/passport"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", prefix+"/realms/room/members/member/experience", nil))
			want := "boosts"
			if useMemberHistory {
				want = "members"
			}
			if w.Code != http.StatusAccepted || w.Header().Get("Route-Family") != want {
				t.Fatalf("history=%v prefix=%s headers=%v status=%d", useMemberHistory, prefix, w.Header(), w.Code)
			}
		}
	}
}

func TestRouteFamiliesKeepOriginalRequestAndAliasResponse(t *testing.T) {
	cases := []struct {
		path, canonical, location string
		direct                    bool
	}{
		{"/api/notifications/devices", "/api/notifications/devices", "/api/result?next=%2F#part", true},
		{"/passport/activities", "/api/activities", "/api/result?next=%2F#part", false},
		{"/sphere/posts/item", "/api/posts/item", "/sphere/result?next=%2F#part", false},
	}
	type contextKey struct{}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.path+"?take=7&opaque=%2F", strings.NewReader("request body"))
			r = r.WithContext(context.WithValue(r.Context(), contextKey{}, "proof-context"))
			r.URL.RawPath = r.URL.Path
			r.Header.Set("Authorization", "Bearer route-proof")
			r.Header.Set("Signature", "signed original request")
			beforeURL, beforeURI := r.URL.String(), r.RequestURI
			handler := http.HandlerFunc(func(w http.ResponseWriter, got *http.Request) {
				if (got == r) != tc.direct || got.URL.Path != tc.canonical || got.URL.RawQuery != r.URL.RawQuery || got.RequestURI != beforeURI || got.Body != r.Body || got.Context().Value(contextKey{}) != "proof-context" {
					t.Fatalf("request changed across boundary: %v", got)
				}
				if got.Header.Get("Authorization") != "Bearer route-proof" || got.Header.Get("Signature") != "signed original request" {
					t.Fatal("credential or signature changed")
				}
				if strings.HasPrefix(tc.path, "/sphere/") && got.URL.RawPath != "" {
					t.Fatal("gateway did not clear stale RawPath")
				}
				if strings.HasPrefix(tc.path, "/passport/") && got.URL.RawPath != tc.path {
					t.Fatal("account alias no longer preserves its existing RawPath contract")
				}
				w.Header().Set("Location", "/api/result?next=%2F#part")
				w.WriteHeader(http.StatusCreated)
			})
			h := router{modules{notificationDevices: handler, activities: handler, posts: handler}}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusCreated || w.Header().Get("Location") != tc.location || r.URL.String() != beforeURL || r.RequestURI != beforeURI {
				t.Fatalf("status=%d headers=%v original=%s", w.Code, w.Header(), r.URL)
			}
		})
	}
}

func TestRouteFamiliesKeepHEADStatusHeadersAndEmptyBody(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatal("HEAD was converted into another method")
		}
		w.Header().Set("Location", "/api/result")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"written":true}`))
	})
	h := router{modules{notificationDevices: handler, activities: handler, posts: handler}}
	for _, tc := range []struct{ path, location string }{
		{"/api/notifications/devices", "/api/result"},
		{"/passport/activities", "/api/result"},
		{"/sphere/posts/item", "/sphere/result"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("HEAD", tc.path, nil))
		if w.Code != http.StatusCreated || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Location") != tc.location {
			t.Fatalf("%s status=%d headers=%v body=%s", tc.path, w.Code, w.Header(), w.Body.String())
		}
	}
}

func TestRouteFamiliesRewriteOnlyLocalCanonicalLocations(t *testing.T) {
	for _, tc := range []struct{ location, want string }{
		{"/api/posts/item?next=%2F#part", "/sphere/posts/item?next=%2F#part"},
		{"https://remote.example/api/posts/item", "https://remote.example/api/posts/item"},
		{"//remote.example/api/posts/item", "//remote.example/api/posts/item"},
		{"/api2/posts/item", "/api2/posts/item"},
		{"relative/posts/item", "relative/posts/item"},
	} {
		h := router{modules{posts: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", tc.location)
			w.WriteHeader(http.StatusCreated)
		})}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/sphere/posts", nil))
		if w.Code != http.StatusCreated || w.Header().Get("Location") != tc.want {
			t.Fatalf("location=%s status=%d headers=%v", tc.location, w.Code, w.Header())
		}
	}
}
