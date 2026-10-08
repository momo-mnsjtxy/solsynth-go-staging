// SPDX-License-Identifier: AGPL-3.0-only
package publisherrating

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGradeSourceThresholds(t *testing.T) {
	for _, tc := range []struct {
		p    float64
		r    int
		want string
	}{{0, 1, "S++"}, {99, 2, "S+"}, {95, 3, "S"}, {90, 4, "A++"}, {80, 5, "A+"}, {70, 6, "A"}, {60, 7, "A-"}, {50, 8, "B+"}, {40, 9, "B"}, {20, 10, "C"}, {19.9, 11, "D"}} {
		if got := Grade(tc.p, tc.r); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}

type fake struct{ Repository }

func (fake) Rating(context.Context, string, string) (int64, error) { return 100, nil }
func TestRatingRouteOwnershipAndPolicyLabel(t *testing.T) {
	for _, p := range []string{"/api/publishers/leaderboard", "/sphere/publishers/name/rating", "/api/publishers/name/rating/history", "/api/publishers/name/rating/overview"} {
		if !Handles(p) {
			t.Fatal(p)
		}
	}
	if Handles("/api/publishers/name/subscription") {
		t.Fatal("stole subscription route")
	}
	w := httptest.NewRecorder()
	NewHandler(fake{}, func(*http.Request, bool) (string, error) { return "", nil }).ServeHTTP(w, httptest.NewRequest("GET", "/api/publishers/name/rating", nil))
	if w.Code != 200 || w.Header().Get("X-Publisher-Rating-Policy") != Policy {
		t.Fatal(w.Code, w.Header())
	}
}
