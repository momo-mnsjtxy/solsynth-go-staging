// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"context"
	"net/http/httptest"
	"testing"
)

type boardDiscoveryFake struct {
	Repository
	calls int
	query BoardDiscoveryQuery
}

func (f *boardDiscoveryFake) DiscoverBoard(_ context.Context, q BoardDiscoveryQuery) ([]BoardDiscoveryApp, error) {
	f.calls++
	f.query = q
	return nil, nil
}
func TestBoardDiscoveryRequestShape(t *testing.T) {
	for _, v := range []struct {
		path, method string
		status       int
		called       bool
	}{{"/api/apps/board", "GET", 200, true}, {"/api/apps/board?take=0&offset=3&slug=app", "GET", 200, true}, {"/api/apps/board?take=101", "GET", 400, false}, {"/api/apps/board?offset=-1", "GET", 400, false}, {"/api/apps/board?take=1&take=2", "GET", 400, false}, {"/api/apps/board?take=", "GET", 400, false}, {"/api/apps/board", "POST", 405, false}} {
		t.Run(v.path+v.method, func(t *testing.T) {
			f := &boardDiscoveryFake{}
			h := (&Service{Repository: f}).Handler()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(v.method, v.path, nil))
			if w.Code != v.status || (f.calls > 0) != v.called {
				t.Fatal(w.Code, w.Body.String(), f.calls)
			}
			if v.called && w.Body.String() != "[]\n" {
				t.Fatal("empty DTO must be array", w.Body.String())
			}
		})
	}
}
