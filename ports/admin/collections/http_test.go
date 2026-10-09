// SPDX-License-Identifier: AGPL-3.0-only
package collections

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fake struct {
	Repository
	updates int
}

func (f *fake) Update(_ context.Context, actor, id string, p Patch) (Collection, error) {
	f.updates++
	return Collection{ID: id, Name: p.Name}, nil
}
func TestCollectionAdminMetadataOnlyInput(t *testing.T) {
	f := &fake{}
	h := NewHandler(f, func(*http.Request, bool) (string, error) { return "synthetic-actor", nil })
	for _, body := range []string{`{"name":"changed"}`, `{"name":"x","publisher_id":"forged"}`, `{"name":"x","name":"y"}`, `{"item_count":99}`, `{"name":"x"} {}`} {
		r := httptest.NewRequest("PATCH", "/api/admin/collections/11111111-1111-4111-8111-111111111111", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 400
		if body == `{"name":"changed"}` {
			want = 200
		}
		if w.Code != want {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if f.updates != 1 {
		t.Fatal(f.updates)
	}
}
func TestCollectionAdminBodyBoundsAndRoutes(t *testing.T) {
	if !Handles("/api/admin/collections") || !Handles("/sphere/admin/collections/11111111-1111-4111-8111-111111111111") || Handles("/api/admin/collections/not-an-id") {
		t.Fatal("route mismatch")
	}
	long := strings.Repeat("😀", 129)
	if (Patch{Name: &long}).Validate() == nil {
		t.Fatal("UTF16 bound not enforced")
	}
	w := httptest.NewRecorder()
	NewHandler(nil, nil).ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/collections", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
