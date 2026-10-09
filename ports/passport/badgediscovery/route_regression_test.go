// SPDX-License-Identifier: AGPL-3.0-only
package badgediscovery

import (
	"net/http/httptest"
	"testing"
)

func TestASCIIRouteCasingMatchesMVC(t *testing.T) {
	h := NewHandler(Config{})
	for _, path := range []string{"/.well-known/badgeſ", "/.well-Known/badges", "/.well-known/badgeſ/icons/draw", "/.well-Known/badges/icons/draw"} {
		if RouteMatches(path) {
			t.Errorf("matched non-ASCII literal %q", path)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Errorf("path %q status %d", path, w.Code)
		}
	}
	for _, path := range []string{"/.WELL-KNOWN/BADGES", "/.Well-Known/Badges/"} {
		if !RouteMatches(path) {
			t.Errorf("rejected ASCII route casing %q", path)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Errorf("path %q status %d", path, w.Code)
		}
	}
}
