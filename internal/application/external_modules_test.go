// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExternalRoutingPreservesSignedCanonicalTarget(t *testing.T) {
	for _, path := range []string{"/activitypub/actors/synthetic/inbox", "/.well-known/webfinger?resource=acct:synthetic@local.example.test"} {
		calls := 0
		h := router{modules{federationPublic: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.RequestURI() != path || r.Host != "local.example.test" || r.Header.Get("Signature") != "synthetic signature" || r.Header.Get("Authorization") != "untouched" {
				t.Fatal("signed request rewritten", r.URL, r.Host, r.Header)
			}
			w.WriteHeader(202)
		})}}
		r := httptest.NewRequest("POST", "https://local.example.test"+path, nil)
		r.Header.Set("Signature", "synthetic signature")
		r.Header.Set("Authorization", "untouched")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 202 || calls != 1 {
			t.Fatal(w.Code, calls)
		}
	}
}
func TestCallRoutesPrecedeRoomRoutes(t *testing.T) {
	for _, prefix := range []string{"/api", "/messager"} {
		h := router{modules{calls: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/chat/realtime/synthetic/join" {
				t.Fatal(r.URL.Path)
			}
			w.WriteHeader(204)
		}), messager: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("call dispatched to chat") })}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", prefix+"/chat/realtime/synthetic/join", nil))
		if w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
}
