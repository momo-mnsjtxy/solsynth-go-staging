// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPetRoutesExposeOnlyCanonicalAndPassportAlias(t *testing.T) {
	var paths []string
	pet := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	h := router{modules{pets: pet}}
	for _, target := range []string{"/api/pets/me", "/passport/pets/me"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: status=%d body=%s", target, w.Code, w.Body.String())
		}
	}
	if len(paths) != 2 || paths[0] != "/api/pets/me" || paths[1] != "/api/pets/me" {
		t.Fatalf("canonical paths: %#v", paths)
	}
	for _, target := range []string{"/sphere/pets/me", "/stargate/pets/me", "/wallet/pets/me", "/api/pet/me"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("unexpected pet alias %s: status=%d body=%s", target, w.Code, w.Body.String())
		}
	}
}

func TestPetRouterFailsClosedAndRejectsAlternatePaths(t *testing.T) {
	for _, target := range []string{"/api/pets", "/api/pets/me", "/passport/pets/me"} {
		w := httptest.NewRecorder()
		(router{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("unconfigured %s: status=%d body=%s", target, w.Code, w.Body.String())
		}
	}
	h := router{modules{pets: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}}
	for _, target := range []string{"/api/pets//me", "/api/pets/../pets/me", "/api/pets/%6de"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("alternate path %s: status=%d body=%s", target, w.Code, w.Body.String())
		}
	}
}
