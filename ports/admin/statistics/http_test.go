// SPDX-License-Identifier: AGPL-3.0-only
package statistics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeStats struct {
	service Service
	calls   int
}

func (f *fakeStats) Statistics(_ context.Context, _ string, s Service) (any, error) {
	f.service = s
	f.calls++
	return map[string]int{"total": 1}, nil
}

type fakeAuth struct{}

func (fakeAuth) Authenticate(*http.Request) (string, error) {
	return "00000000-0000-4000-8000-000000000001", nil
}
func TestStatsRequireExplicitService(t *testing.T) {
	for _, tt := range []struct {
		configured Service
		path       string
		want       int
		selected   Service
	}{{"", "/api/admin/stats", 404, ""}, {"", "/api/admin/sphere/stats", 200, Sphere}, {Passport, "/api/admin/stats", 200, Passport}, {Wallet, "/api/admin/sphere/stats", 404, ""}, {Wallet, "/api/admin/stats?scope=passport", 400, ""}} {
		f := &fakeStats{}
		w := httptest.NewRecorder()
		NewHandler(f, fakeAuth{}, tt.configured).ServeHTTP(w, httptest.NewRequest("GET", tt.path, nil))
		if w.Code != tt.want || f.service != tt.selected {
			t.Fatal(tt, w.Code, f.service)
		}
	}
}
