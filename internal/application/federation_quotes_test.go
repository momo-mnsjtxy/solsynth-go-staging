// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuoteAuthorizationHostUsesPrivateMutationBoundary(t *testing.T) {
	public := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Boundary", "public") })
	private := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Boundary", "private") })
	h := router{modules{federationPublic: public, federationPrivate: private}}
	for _, tc := range []struct{ method, path, boundary string }{{"GET", "/quote-authorizations/00000000-0000-4000-8000-000000000001", "public"}, {"DELETE", "/quote-authorizations/00000000-0000-4000-8000-000000000001", "private"}, {"POST", "/quote-authorizations", "private"}, {"POST", "/activitypub/actors/alice/inbox", "public"}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Boundary") != tc.boundary || r.URL.Path != tc.path {
			t.Fatal(tc, w.Header())
		}
	}
}
