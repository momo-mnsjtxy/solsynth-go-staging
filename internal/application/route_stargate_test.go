// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMovedStargateResourcesReachTheirExistingDomain(t *testing.T) {
	h := router{modules{
		accounts: routeFamilyMarker("accounts"), board: routeFamilyMarker("board"),
		accountHistory: routeFamilyMarker("history"), accountSettings: routeFamilyMarker("settings"),
		accountConnections: routeFamilyMarker("connections"), friends: routeFamilyMarker("friends"),
		presence: routeFamilyMarker("presence"), activities: routeFamilyMarker("activities"),
		presenceSensors: routeFamilyMarker("sensors"), progression: routeFamilyMarker("progression"),
		social: routeFamilyMarker("social"), realmMembers: routeFamilyMarker("members"),
		realmBoosts: routeFamilyMarker("boosts"), calendar: routeFamilyMarker("calendar"),
		locations: routeFamilyMarker("locations"), artwork: routeFamilyMarker("artwork"),
		fortunes: routeFamilyMarker("fortunes"), ipcheck: routeFamilyMarker("ipcheck"),
		wallet: routeFamilyMarker("wallet"), commerce: routeFamilyMarker("commerce"),
		merchants: routeFamilyMarker("merchants"), public: routeFamilyMarker("public"),
	}}
	for _, tc := range []struct{ path, family string }{
		{"/accounts/me/board", "board"}, {"/accounts/alice/board", "board"},
		{"/accounts/me/connections/provider", "connections"},
		{"/accounts/me/leveling", "progression"}, {"/accounts/me/check-in", "progression"},
		{"/accounts/me/progression/achievements", "progression"}, {"/admin/progression/test-ws-packet", "progression"},
		{"/accounts/alice/statuses", "presence"}, {"/accounts/me/presence/sensors", "sensors"},
		{"/accounts/me/calendar", "calendar"}, {"/notable-days", "calendar"},
		{"/friends/overview", "friends"}, {"/activities/alice", "activities"},
		{"/presence/artworks/example.webp", "artwork"},
		{"/realms", "social"}, {"/realms/room/members?label_id=label", "members"},
		{"/realms/room/boosts", "boosts"}, {"/nfc/tags", "locations"},
		{"/admin/nfc/tags", "locations"}, {"/ip-check/geo", "ipcheck"}, {"/fortune", "fortunes"},
		{"/wallets/all", "wallet"}, {"/wallets/transfer", "wallet"},
		{"/accounts/me/pin-status", "wallet"}, {"/orders/order/pay", "commerce"},
		{"/subscriptions/gifts/sent", "commerce"}, {"/subscriptions/billing", "merchants"},
		{"/admin/payments/orders", "merchants"}, {"/admin/wallet-products/golds-resupply-pack", "merchants"},
		{"/wallet-products/catalog", "public"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/stargate"+tc.path, nil)
			before := r.URL.String()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusAccepted || w.Header().Get("Route-Family") != tc.family ||
				w.Header().Get("Route-Path") != "/api"+strings.Split(tc.path, "?")[0] || r.URL.String() != before {
				t.Fatalf("status=%d headers=%v original=%s", w.Code, w.Header(), r.URL)
			}
		})
	}
}

func TestMovedStargateResourcesPreserveNativeProtocols(t *testing.T) {
	for _, p := range []string{
		"/stargate/accounts", "/stargate/accounts/me", "/stargate/accounts/verify",
		"/stargate/accounts/me/password", "/stargate/accounts/me/activation/progress",
		"/stargate/auth/token", "/stargate/auth/challenge", "/stargate/factors",
		"/stargate/sessions", "/stargate/connect/authorize", "/stargate/e2ee/mls/kp/status",
		"/stargate/posts", "/stargate/private/apps", "/stargate/wallets-other",
	} {
		if canonical, ok := movedStargatePath(p); ok {
			t.Fatalf("protocol or unrelated resource rewritten: %s -> %s", p, canonical)
		}
	}
}

func TestMovedStargateResourcesPreserveRequestAndResponse(t *testing.T) {
	r := httptest.NewRequest("POST", "/stargate/wallets/transfer?opaque=%2F", strings.NewReader("request"))
	r.URL.RawPath = r.URL.Path
	r.Header.Set("Idempotency-Key", "synthetic-transfer")
	uri := r.RequestURI
	h := router{modules{wallet: http.HandlerFunc(func(w http.ResponseWriter, got *http.Request) {
		if got.URL.Path != "/api/wallets/transfer" || got.URL.RawPath != "" || got.URL.RawQuery != "opaque=%2F" ||
			got.RequestURI != uri || got.Body != r.Body || got.Header.Get("Idempotency-Key") != "synthetic-transfer" {
			t.Fatal("request lost its original body, query, target or idempotency key")
		}
		w.Header().Set("Location", "/api/wallets/result?opaque=%2F")
		w.WriteHeader(http.StatusCreated)
	})}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated || w.Header().Get("Location") != "/stargate/wallets/result?opaque=%2F" || r.URL.Path != "/stargate/wallets/transfer" {
		t.Fatalf("status=%d headers=%v", w.Code, w.Header())
	}
	for _, p := range []string{"/stargate//wallets", "/stargate/wallets/../orders", "/stargate/wallets%2Ftransfer"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("ambiguous moved path accepted: %s status=%d", p, w.Code)
		}
	}
}
