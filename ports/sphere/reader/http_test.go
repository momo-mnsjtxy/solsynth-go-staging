// SPDX-License-Identifier: AGPL-3.0-only
package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

type fakeRepository struct {
	previews, invalidations int
	raw, actor              string
}

func (r *fakeRepository) Preview(c context.Context, raw string) (Link, error) {
	r.previews++
	r.raw = raw
	return Link{URL: raw, Title: "Synthetic preview"}, nil
}
func (r *fakeRepository) Invalidate(c context.Context, actor, raw string) error {
	r.invalidations++
	r.raw = raw
	r.actor = actor
	return nil
}
func (r *fakeRepository) InvalidateAll(c context.Context, actor string) error {
	r.invalidations++
	r.actor = actor
	return nil
}

type fakeAuth struct {
	actor      string
	err        error
	permission *string
}

func (a fakeAuth) Authenticate(*http.Request) (string, error) { return a.actor, nil }
func (a fakeAuth) Authorize(r *http.Request, key string) error {
	if a.permission != nil {
		*a.permission = key
	}
	return a.err
}
func TestHTTPRoutesQueryAndAuthority(t *testing.T) {
	r := &fakeRepository{}
	permission := ""
	h := NewHandler(r, fakeAuth{actor: "synthetic", permission: &permission})
	raw := "https://preview.example/Path?q=%2F+Kept"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/scrap/link?url="+url.QueryEscape(raw), nil))
	if w.Code != 200 || r.raw != raw || r.previews != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String(), r)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/scrap/link/cache?url="+url.QueryEscape(raw), nil))
	if w.Code != 200 || r.invalidations != 1 || permission != "cache.scrap" || r.raw != raw {
		t.Fatal(w.Code, w.Body.String(), r, permission)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/scrap/cache/all", nil))
	if w.Code != 200 || r.invalidations != 2 {
		t.Fatal(w.Code, r)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{{"GET", "/api/scrap/link", "", 400}, {"GET", "/api/scrap/link?url=x&url=y", "", 400}, {"GET", "/api/scrap/link?url=x&token=y", "", 400}, {"GET", "/api/scrap/link?url=%zz", "", 400}, {"GET", "/api/scrap/link?url=x", "{}", 400}, {"DELETE", "/api/scrap/cache/all?url=x", "", 400}, {"POST", "/api/scrap/link?url=x", "", 405}, {"GET", "/api/scrap/article?url=x", "", 404}} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	if r.previews != 1 || r.invalidations != 2 {
		t.Fatal("invalid request reached repository", r)
	}
	for _, tc := range []struct {
		auth   Auth
		status int
	}{{nil, 401}, {fakeAuth{}, 401}, {fakeAuth{actor: "synthetic", err: authority.ErrForbidden}, 403}} {
		w = httptest.NewRecorder()
		NewHandler(r, tc.auth).ServeHTTP(w, httptest.NewRequest("DELETE", "/api/scrap/cache/all", nil))
		if w.Code != tc.status || r.invalidations != 2 {
			t.Fatal(w.Code, r)
		}
	}
}
