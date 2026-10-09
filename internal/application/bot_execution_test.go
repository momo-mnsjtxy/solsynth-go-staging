// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBotCommandGatewayRouting(t *testing.T) {
	for _, p := range []string{"/api/chat/00000000-0000-4000-8000-000000000001/bots/commands", "/messager/chat/00000000-0000-4000-8000-000000000001/bots/commands"} {
		called := false
		h := router{modules{bots: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if r.URL.Path != "/api/chat/00000000-0000-4000-8000-000000000001/bots/commands" {
				t.Fatal(r.URL.Path)
			}
			w.WriteHeader(204)
		})}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if !called || w.Code != 204 {
			t.Fatal(p, w.Code)
		}
	}
	for _, p := range []string{"/develop/chat/room/bots/commands", "/messager/private/bots/id/execution/room"} {
		h := router{modules{bots: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("wrong service reached bot handler") })}}
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
}
