// SPDX-License-Identifier: AGPL-3.0-only
package content

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"testing"
)

const testID = "00000000-0000-4000-8000-000000000001"

type testRepo struct {
	calls int
	cmd   Command
	kind  Kind
	query Query
}

func (f *testRepo) List(_ context.Context, _ string, k Kind, q Query) (any, int64, error) {
	f.calls++
	f.kind = k
	f.query = q
	return []Post{}, 3, nil
}
func (f *testRepo) Get(context.Context, string, Kind, string) (any, error) {
	f.calls++
	return Post{}, nil
}
func (f *testRepo) Mutate(_ context.Context, _ string, k Kind, _ string, c Command) (any, error) {
	f.calls++
	f.kind = k
	f.cmd = c
	return map[string]bool{"success": true}, nil
}
func (f *testRepo) Members(context.Context, string, string, Query) ([]social.Member, int64, error) {
	f.calls++
	return []social.Member{}, 0, nil
}
func (f *testRepo) ModerateRealmPost(context.Context, string, string, *string) (Post, error) {
	f.calls++
	return Post{}, nil
}

type testAuth struct{}

func (testAuth) Authenticate(*http.Request) (string, error) { return testID, nil }
func send(t *testing.T, f *testRepo, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	NewHandler(f, testAuth{}).ServeHTTP(w, r)
	return w
}
func TestAdminContentRoutesAndStrictBodies(t *testing.T) {
	f := &testRepo{}
	w := send(t, f, "POST", "/api/admin/posts/"+testID+"/visibility", `{"visibility":"private"}`)
	if w.Code != 200 || f.cmd.Visibility == nil || *f.cmd.Visibility != 3 {
		t.Fatal(w.Code, f.cmd)
	}
	for _, body := range []string{`{"visibility":0,"visibility":3}`, `{"visibility":0,"grant":"*"}`, `{"visibility":5}`, `{"visibility":null}`, `{"visibility":0} {}`, `[0]`} {
		before := f.calls
		w = send(t, f, "POST", "/api/admin/posts/"+testID+"/visibility", body)
		if w.Code != 400 || f.calls != before {
			t.Fatal(body, w.Code, f.calls)
		}
	}
	w = send(t, f, "GET", "/api/admin/posts?locked=true&visibility=public&take=12", "")
	if w.Code != 200 || w.Header().Get("X-Total") != "3" || f.query.Take != 12 || f.query.Locked == nil || !*f.query.Locked {
		t.Fatal(w.Code, w.Header())
	}
	for _, q := range []string{"take=201", "take=1&take=2", "publisher_id=invalid", "role=50", "query=a&unexpected=b", "shadowban_reason=unknown"} {
		before := f.calls
		w = send(t, f, "GET", "/api/admin/posts?"+q, "")
		if w.Code != 400 || f.calls != before {
			t.Fatal(q, w.Code)
		}
	}
	w = send(t, f, "POST", "/api/admin/posts/"+testID+"/lock/batch", `["`+testID+`"]`)
	if w.Code != 200 || f.cmd.Action != "lock" || len(f.cmd.IDs) != 1 {
		t.Fatal(w.Code, f.cmd)
	}
	w = send(t, f, "PATCH", "/api/admin/realms/example/members/"+testID+"/role", `{"role":50}`)
	if w.Code != 200 || f.cmd.AccountID != testID || f.cmd.Role == nil || *f.cmd.Role != 50 {
		t.Fatal(w.Code, f.cmd)
	}
	w = send(t, f, "POST", "/api/admin/publishers/example/verification", `{}`)
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
}
func TestAdminContentDoesNotCaptureTaxonomyOrSimilarPrefixes(t *testing.T) {
	for _, path := range []string{"/api/admin/posts/tags", "/api/admin/posts/categories", "/api/admin/postsuffix", "/api/admin/realmsuffix", "/api/posts/" + testID} {
		if Handles(path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/api/admin/posts", "/api/admin/posts/" + testID + "/lock", "/api/admin/posts/lock/batch", "/api/posts/" + testID + "/realm/moderate"} {
		if !Handles(path) {
			t.Fatal(path)
		}
	}
}
