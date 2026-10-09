// SPDX-License-Identifier: AGPL-3.0-only
package blogshttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/sphere/blogs"
)

type fakeRepository struct {
	calls                    int
	action, selector, domain string
}

func (f *fakeRepository) List(_ context.Context, _, selector string) ([]blogs.Domain, error) {
	f.calls++
	f.action, f.selector = "list", selector
	return []blogs.Domain{}, nil
}
func (f *fakeRepository) Add(_ context.Context, _, selector, domain string) (blogs.Domain, error) {
	f.calls++
	f.action, f.selector, f.domain = "add", selector, domain
	return blogs.Domain{ID: "00000000-0000-4000-8000-000000000002", Domain: domain}, nil
}
func (f *fakeRepository) Recheck(_ context.Context, _, selector, id string) (blogs.Domain, error) {
	f.calls++
	f.action, f.selector = "recheck", selector
	return blogs.Domain{ID: id}, nil
}
func (f *fakeRepository) Remove(_ context.Context, _, selector, id string) error {
	f.calls++
	f.action, f.selector = "remove", selector
	return nil
}

type fakeAuth string

func (a fakeAuth) Authenticate(*http.Request) (string, error) { return string(a), nil }

func TestDomainHTTPRoutesAndRejectedRequests(t *testing.T) {
	const actor = "00000000-0000-4000-8000-000000000001"
	const id = "00000000-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		method, suffix, body, actor, action string
		status                              int
	}{
		{"GET", "", "", actor, "list", 200},
		{"POST", "", `{"domain":"blog.example"}`, actor, "add", 200},
		{"DELETE", "/" + id, "", actor, "remove", 204},
		{"POST", "/" + id + "/recheck", "", actor, "recheck", 200},
		{"GET", "", "", "", "", 401},
		{"POST", "", `{"domain":"blog.example","domain":"other.example"}`, actor, "", 400},
		{"POST", "", `{"domain":"blog.example","status":1}`, actor, "", 400},
		{"POST", "/" + id + "/recheck", `{"status":1}`, actor, "", 400},
		{"GET", "?unexpected=true", "", actor, "", 400},
	} {
		rp := &fakeRepository{}
		r := httptest.NewRequest(tc.method, "/api/publishers/synthetic/domains"+tc.suffix, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		NewHandler(rp, fakeAuth(tc.actor)).ServeHTTP(w, r)
		if w.Code != tc.status || rp.action != tc.action || tc.action == "" && rp.calls != 0 || tc.action != "" && (rp.calls != 1 || rp.selector != "synthetic") {
			t.Fatal(tc, w.Code, w.Body.String(), rp)
		}
	}
}
