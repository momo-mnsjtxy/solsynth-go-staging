// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testActor = "11111111-1111-4111-8111-111111111111"

type fakeAuth struct {
	actor                   string
	authenticate, authorize error
	permission              string
}

func (a *fakeAuth) Authenticate(*http.Request) (string, error) { return a.actor, a.authenticate }
func (a *fakeAuth) Authorize(_ *http.Request, p string) error  { a.permission = p; return a.authorize }

type fakeRepo struct {
	calls    int
	patch    Patch
	selector string
	options  ListOptions
}

func (r *fakeRepo) Create(_ context.Context, actor, selector string, v Patch) (Post, error) {
	r.calls++
	r.patch = v
	r.selector = selector
	return Post{ID: testActor}, nil
}
func (r *fakeRepo) Update(_ context.Context, actor, id string, v Patch) (Post, error) {
	r.calls++
	r.patch = v
	return Post{ID: id}, nil
}
func (r *fakeRepo) Publish(_ context.Context, actor, id string) (Post, error) {
	r.calls++
	return Post{ID: id}, nil
}
func (r *fakeRepo) Delete(context.Context, string, string) error      { r.calls++; return nil }
func (r *fakeRepo) Get(context.Context, string, string) (Post, error) { r.calls++; return Post{}, nil }
func (r *fakeRepo) GetSlug(context.Context, string, string, string) (Post, error) {
	r.calls++
	return Post{}, nil
}
func (r *fakeRepo) List(_ context.Context, _ string, o ListOptions) ([]Post, int64, error) {
	r.calls++
	r.options = o
	return []Post{}, 0, nil
}
func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPRejectsUnsupportedAndAmbiguousInputBeforeRepository(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"content":"a","content":"b"}`, `{"content":"a"} {}`, `{"content":"a","publisher_id":"other"}`, `{"content":"a","attachments":["file"]}`, `{"content":"a","realm_id":"realm"}`, `{"content":"a","auto_chain":true}`, `{"content":"a","meta":{"secret":"x"}}`, `{"content":"a","collection_ids":["x"]}`, `{"content":"a","future_field":0}`, `{"content":"a","visibility":"0"}`} {
		r := &fakeRepo{}
		h := NewHandler(r, &fakeAuth{actor: testActor})
		w := request(h, "POST", "/api/posts", body)
		if w.Code != 400 && w.Code != 501 || r.calls != 0 {
			t.Fatalf("accepted %s: %d %d", body, w.Code, r.calls)
		}
	}
	r := &fakeRepo{}
	a := &fakeAuth{actor: testActor}
	h := NewHandler(r, a)
	w := request(h, "POST", "/api/posts?pub=alice", `{"content":"text","attachments":[],"tags":null,"meta":{},"auto_chain":false,"realm_id":null}`)
	if w.Code != 200 || r.calls != 1 || r.selector != "alice" || r.patch.Content == nil || *r.patch.Content != "text" || a.permission != "posts.create" {
		t.Fatal(w.Code, w.Body.String(), r)
	}
}
func TestHTTPAuthenticationAndPermissionsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		auth   *fakeAuth
		status int
	}{{&fakeAuth{}, 401}, {&fakeAuth{actor: testActor, authenticate: ErrUnavailable}, 503}, {&fakeAuth{actor: testActor, authorize: ErrForbidden}, 403}, {&fakeAuth{actor: "not-an-account"}, 401}} {
		r := &fakeRepo{}
		w := request(NewHandler(r, tc.auth), "POST", "/api/posts", `{"content":"text"}`)
		if w.Code != tc.status || r.calls != 0 {
			t.Fatal(w.Code, w.Body.String(), r.calls)
		}
	}
	if w := request(NewHandler(nil, nil), "GET", "/api/posts", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	for _, tc := range []struct {
		method, path, body, permission string
		status                         int
	}{{"GET", "/api/posts/drafts", "", "posts.view", 200}, {"PATCH", "/api/posts/" + testActor, `{"title":"x"}`, "posts.update", 200}, {"DELETE", "/api/posts/" + testActor, "", "posts.delete", 204}, {"POST", "/api/posts/" + testActor + "/publish", "", "posts.create", 200}} {
		a := &fakeAuth{actor: testActor}
		w := request(NewHandler(&fakeRepo{}, a), tc.method, tc.path, tc.body)
		if w.Code != tc.status || a.permission != tc.permission {
			t.Fatal(tc, w.Code, a.permission)
		}
	}
}
func TestHTTPListingContractAndUnsupportedFilters(t *testing.T) {
	r := &fakeRepo{}
	h := NewHandler(r, &fakeAuth{})
	w := request(h, "GET", "/api/posts?take=5&offset=3&type=1&order=date&orderDesc=false", "")
	if w.Code != 200 || w.Header().Get("X-Total") != "0" || w.Body.String() != "[]\n" || w.Header().Get("Cache-Control") != "no-store" || r.options.Take != 5 || !r.options.Ascending {
		t.Fatal(w.Code, w.Body.String(), r.options)
	}
	for _, q := range []string{"take=0", "take=101", "offset=-1", "take=2&take=3", "query=term", "pub=alice,bob", "order=popularity", "offset=x", "broken=%xx"} {
		r.calls = 0
		w = request(h, "GET", "/api/posts?"+q, "")
		if w.Code != 400 && w.Code != 501 || r.calls != 0 {
			t.Fatal(q, w.Code, r.calls)
		}
	}
}
func TestTextValidationAndStateTransitions(t *testing.T) {
	text := "hello"
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	for _, tc := range []struct {
		patch Patch
		want  error
	}{{Patch{}, ErrInvalid}, {Patch{Content: &text, Type: ptr(Blog)}, ErrUnsupported}, {Patch{Content: &text, Visibility: ptr(Friends)}, ErrUnsupported}, {Patch{Content: &text, Visibility: ptr(CloseFriends)}, ErrUnsupported}, {Patch{Content: &text, Visibility: ptr(QuietPublic)}, ErrUnsupported}, {Patch{Content: &text, PublishedAt: &future}, ErrUnsupported}} {
		if e := ValidatePatch(tc.patch, true, now); !errors.Is(e, tc.want) {
			t.Fatal(e, tc.want)
		}
	}
	for _, v := range []int{Public, Unlisted, Private} {
		for _, typ := range []int{Moment, Article} {
			if e := ValidatePatch(Patch{Content: &text, Type: &typ, Visibility: &v}, true, now); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := ValidatePatch(Patch{Content: &text, DraftedAt: &now, PublishedAt: &now}, true, now); e == nil {
		t.Fatal("conflicting state accepted")
	}
}
func ptr(v int) *int { return &v }
