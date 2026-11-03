// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"strings"
	"testing"
)

type blogRepo struct {
	Repository
	calls int
}

func TestNativeSolianLinksUseHostBoundary(t *testing.T) {
	for _, raw := range []string{"https://solian.app/posts/1", "https://SOLIAN.APP/posts/1", "https://www.solian.app/", "https://sub.solian.app/posts/1"} {
		if !IsNativeSolianLink(raw) {
			t.Fatal("native Solian link was not recognized", raw)
		}
	}
	for _, raw := range []string{"https://solian.app.evil.example/", "https://evils olian.app/", "https://evilsolian.app/", "https://solian.app@evil.example/", "https://solian.app.evil.example/solian.app", "https://other.example/?url=https://solian.app/", "javascript://solian.app/", "/solian.app/posts/1"} {
		if IsNativeSolianLink(raw) {
			t.Fatal("foreign or malformed link treated as native", raw)
		}
	}
}

func (r *blogRepo) CheckBlogPermission(_ context.Context, actor, pub, url string) (blogs.Permission, error) {
	r.calls++
	return blogs.Permission{CanCreate: true, Domain: "blog.example", PublisherName: pub}, nil
}

type blogAuth string

func (a blogAuth) Authenticate(*http.Request) (string, error) { return string(a), nil }
func (a blogAuth) Authorize(*http.Request, string) error      { return nil }
func TestBlogPermissionRouteRequiresAuthenticatedStrictEnvelope(t *testing.T) {
	for _, tc := range []struct {
		body, query, actor string
		want               int
	}{
		{`{"url":"https://blog.example/a"}`, "pub=synthetic", "00000000-0000-4000-8000-000000000001", 200},
		{`{"url":"https://blog.example/a"}`, "", "", 401},
		{`null`, "", "00000000-0000-4000-8000-000000000001", 400},
		{`{"url":"https://blog.example/a","url":"b"}`, "", "00000000-0000-4000-8000-000000000001", 400},
		{`{"url":"https://blog.example/a"}`, "pub=a&pub=b", "00000000-0000-4000-8000-000000000001", 400},
		{`{"url":"https://blog.example/a","is_superuser":true}`, "", "00000000-0000-4000-8000-000000000001", 400},
	} {
		repo := &blogRepo{}
		r := httptest.NewRequest("POST", "/api/posts/blog/check-permission?"+tc.query, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		NewHandler(repo, blogAuth(tc.actor)).ServeHTTP(w, r)
		if w.Code != tc.want || tc.want != 200 && repo.calls != 0 {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}
