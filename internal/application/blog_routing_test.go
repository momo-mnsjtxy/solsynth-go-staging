// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublisherBlogDomainRouteDoesNotFallIntoAccountModule(t *testing.T) {
	for _, p := range []string{"/api/publishers/synthetic/domains", "/sphere/publishers/synthetic/domains/id/recheck"} {
		h := router{modules{blogs: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path[:5] != "/api/" {
				t.Fatal(r.URL.Path)
			}
			w.WriteHeader(202)
		}), accounts: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("domain route fell through to account handler") })}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != 202 {
			t.Fatal(p, w.Code)
		}
	}
}
