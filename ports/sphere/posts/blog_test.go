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
