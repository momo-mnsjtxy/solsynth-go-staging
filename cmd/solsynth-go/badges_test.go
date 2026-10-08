// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"solsynth.local/go-migration/ports/passport/badgediscovery"
)

func TestBadgePublicRouterAndAssetDirectory(t *testing.T) {
	dir := t.TempDir()
	icon := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8"/></svg>`)
	if err := os.WriteFile(filepath.Join(dir, "whatshot.svg"), icon, 0600); err != nil {
		t.Fatal(err)
	}
	watchCalls := 0
	watch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { watchCalls++; w.WriteHeader(http.StatusUnauthorized) })
	h := newAPIHandler(watch, nil, badgediscovery.NewHandler(badgediscovery.Config{IconsPath: dir}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/.well-known/badges", nil))
	if rr.Code != 200 {
		t.Fatalf("manifest status %d", rr.Code)
	}
	var m badgediscovery.Manifest
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || len(m.Badges) != 23 {
		t.Fatalf("manifest version/count: %d/%d", m.Version, len(m.Badges))
	}
	for _, b := range m.Badges {
		if b.IconURL == nil {
			t.Fatalf("missing icon URL: %s", b.Identifier)
		}
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/.well-known/badges/icons/whatshot", nil))
	if rr.Code != 200 || rr.Body.String() != string(icon) || rr.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("icon response: %d %s", rr.Code, rr.Body.String())
	}
	if watchCalls != 0 {
		t.Fatal("anonymous badges dispatched through watch")
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/posts/watch", nil))
	if rr.Code != 401 || watchCalls != 1 {
		t.Fatal("watch authentication boundary changed")
	}
	for _, method := range []string{"HEAD", "POST"} {
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, "/.well-known/badges", nil))
		if rr.Code != 405 || rr.Header().Get("Allow") != "GET" || rr.Body.Len() != 0 {
			t.Fatalf("%s method behavior", method)
		}
	}
}

func TestBadgeUnmatchedRoutesStayEmpty404(t *testing.T) {
	h := newAPIHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("unmatched route reached watch") }), nil, badgediscovery.NewHandler(badgediscovery.Config{}))
	for _, path := range []string{"/.well-known/badges//", "/.well-known/badges/icons", "/.well-known/badges/icons/", "/.well-known/badges/icons/a/b"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 404 || rr.Body.Len() != 0 || rr.Header().Get("Cache-Control") != "" {
			t.Fatalf("route %s got %d %q", path, rr.Code, rr.Body.String())
		}
	}
}
