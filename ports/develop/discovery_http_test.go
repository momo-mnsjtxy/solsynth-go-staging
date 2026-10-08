// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type discoveryReaderFunc func(context.Context, Query) (Page, error)

func (f discoveryReaderFunc) Discover(ctx context.Context, q Query) (Page, error) { return f(ctx, q) }

func TestDiscoveryNormalConstructionFailsClosed(t *testing.T) {
	reader := discoveryReaderFunc(func(context.Context, Query) (Page, error) { t.Fatal("queried storage"); return Page{}, nil })
	for _, reader := range []Reader{reader, nil} {
		w := httptest.NewRecorder()
		NewHandler(reader).ServeHTTP(w, httptest.NewRequest("GET", "/api/apps", nil))
		if w.Code != 503 || !strings.Contains(w.Body.String(), "discovery_policy_unverified") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestDiscoveryCandidateResponse(t *testing.T) {
	description := ""
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "retained")
	cases := []struct {
		name, url string
		query     Query
		page      Page
		body      string
	}{
		{"defaults empty", "/api/apps", Query{Take: 20}, Page{Total: 7}, "[]\n"},
		{"zero take", "/api/apps?take=0&offset=2147483647", Query{Offset: 2147483647}, Page{}, "[]\n"},
		{"dto null and empty descriptions", "/api/apps?take=100&offset=2&search=", Query{Take: 100, Offset: 2, Search: &description}, Page{Total: 21, Apps: []AppDiscovery{
			{ID: "id1", Slug: "app", Title: "A", ProductsCount: 2, WidgetsCount: 3},
			{ID: "id2", Slug: "app2", Title: "B", Description: &description},
		}}, "[{\"id\":\"id1\",\"slug\":\"app\",\"title\":\"A\",\"description\":null,\"products_count\":2,\"widgets_count\":3},{\"id\":\"id2\",\"slug\":\"app2\",\"title\":\"B\",\"description\":\"\",\"products_count\":0,\"widgets_count\":0}]\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			reader := discoveryReaderFunc(func(gotCtx context.Context, q Query) (Page, error) {
				calls++
				if !reflect.DeepEqual(q, tt.query) {
					t.Fatalf("query %#v want %#v", q, tt.query)
				}
				if gotCtx.Value(contextKey{}) != "retained" {
					t.Fatal("lost request context")
				}
				return tt.page, nil
			})
			w := httptest.NewRecorder()
			NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest("GET", tt.url, nil).WithContext(ctx))
			if w.Code != 200 || calls != 1 || w.Body.String() != tt.body || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("%d %s calls %d", w.Code, w.Body.String(), calls)
			}
			var total int32
			if err := json.Unmarshal([]byte(w.Header().Get("X-Total")), &total); err != nil || total != tt.page.Total {
				t.Fatalf("total %d err %v", total, err)
			}
		})
	}
}

func TestDiscoveryQueryRejectsBeforeRead(t *testing.T) {
	reader := discoveryReaderFunc(func(context.Context, Query) (Page, error) { t.Fatal("queried storage"); return Page{}, nil })
	for _, query := range []string{"take=-1", "take=101", "offset=-1", "take=2147483648", "take=-2147483649", "offset=2147483648", "offset=-2147483649", "take=1.0", "take=x", "offset=x", "take=", "offset=", "take=1&take=2", "offset=0&offset=1", "search=a&search=b", "take=1;offset=0", "search=%zz", "take=0x10", "take=1_0", "take=%201"} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest("GET", "/api/apps?"+query, nil))
			if w.Code != 400 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDiscoveryCandidateRouteAndIdentityBoundary(t *testing.T) {
	reader := discoveryReaderFunc(func(context.Context, Query) (Page, error) { t.Fatal("queried storage"); return Page{}, nil })
	for _, path := range []string{"/api/apps/", "/api/apps/app", "/api/bots", "/"} {
		w := httptest.NewRecorder()
		NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("path %s status %d", path, w.Code)
		}
	}
	for _, method := range []string{"HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		w := httptest.NewRecorder()
		NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest(method, "/api/apps", nil))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Fatalf("method %s status %d", method, w.Code)
		}
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account", "authorization", "cOoKiE"} {
		for _, value := range []string{"", "untrusted"} {
			r := httptest.NewRequest("GET", "/api/apps", nil)
			r.Header[header] = []string{value}
			w := httptest.NewRecorder()
			NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("header %s status %d", header, w.Code)
			}
		}
	}
}

func TestDiscoveryUnavailableAndCancellation(t *testing.T) {
	for _, reader := range []Reader{nil, discoveryReaderFunc(func(context.Context, Query) (Page, error) { return Page{}, errors.New("secret SQL DSN password") }), discoveryReaderFunc(func(context.Context, Query) (Page, error) { return Page{Total: -1}, nil })} {
		w := httptest.NewRecorder()
		NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest("GET", "/api/apps", nil))
		if w.Code != 503 || w.Body.String() != "{\"error\":\"discovery_unavailable\"}\n" || w.Header().Get("X-Total") != "" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := discoveryReaderFunc(func(context.Context, Query) (Page, error) { t.Fatal("queried after cancellation"); return Page{}, nil })
	w := httptest.NewRecorder()
	NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest("GET", "/api/apps", nil).WithContext(ctx))
	if w.Code != 503 {
		t.Fatalf("status %d", w.Code)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	reader = discoveryReaderFunc(func(ctx context.Context, _ Query) (Page, error) {
		cancel()
		return Page{Apps: []AppDiscovery{{ID: "must not leak"}}, Total: 1}, nil
	})
	w = httptest.NewRecorder()
	NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest("GET", "/api/apps", nil).WithContext(ctx))
	if w.Code != 503 || strings.Contains(w.Body.String(), "must not leak") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestDiscoverySearchForwardedWithoutModification(t *testing.T) {
	reader := discoveryReaderFunc(func(_ context.Context, q Query) (Page, error) {
		if q.Search == nil || *q.Search != "  %_猫  " {
			t.Fatalf("search %#v", q.Search)
		}
		return Page{}, nil
	})
	w := httptest.NewRecorder()
	NewSyntheticCompatibilityHandler(reader).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/apps?search=%20%20%25_%E7%8C%AB%20%20", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
