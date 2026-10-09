// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostPinCanonicalAndSphereAliases(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		for _, path := range []string{"/api/posts/11111111-1111-4111-8111-111111111111/pin", "/sphere/posts/11111111-1111-4111-8111-111111111111/pin"} {
			calls := 0
			h := router{modules{posts: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != method || r.URL.Path != "/api/posts/11111111-1111-4111-8111-111111111111/pin" {
					t.Fatal(r.Method, r.URL.Path)
				}
				w.WriteHeader(202)
			})}}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if w.Code != 202 || calls != 1 {
				t.Fatal(method, path, w.Code, calls)
			}
		}
	}
}
