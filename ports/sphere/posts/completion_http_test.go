// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"context"
	"testing"
)

type completionFake struct {
	fakeRepo
	operation, actor, id, name, mode string
	offset, take                     int
	content                          *string
}

func (f *completionFake) BoostPost(_ context.Context, a, id string, c *string) (Boost, error) {
	f.operation, f.actor, f.id, f.content = "boost", a, id, c
	return Boost{PostID: id, AccountID: a, Content: c}, nil
}
func (f *completionFake) UnboostPost(_ context.Context, a, id string) error {
	f.operation, f.actor, f.id = "unboost", a, id
	return nil
}
func (f *completionFake) PostBoosts(_ context.Context, a, id string, o, n int) ([]Boost, int64, error) {
	f.operation, f.actor, f.id, f.offset, f.take = "boosts", a, id, o, n
	return []Boost{}, 3, nil
}
func (f *completionFake) Featured(_ context.Context, a string, o, n int) ([]Post, int64, error) {
	f.operation, f.actor, f.offset, f.take = "featured", a, o, n
	return []Post{}, 4, nil
}
func (f *completionFake) Adjacent(_ context.Context, a, id string, next bool, o ListOptions) (Post, error) {
	f.operation, f.actor, f.id = "prev", a, id
	if next {
		f.operation = "next"
	}
	f.options = o
	return Post{ID: id}, nil
}
func (f *completionFake) ReactionUser(_ context.Context, a, name string, o, n int) ([]ReactionListing, int64, error) {
	f.operation, f.actor, f.name, f.offset, f.take = "reaction-user", a, name, o, n
	return []ReactionListing{}, 5, nil
}
func (f *completionFake) Replies(_ context.Context, a, id, mode string, o, n int, ascending bool) ([]Post, int64, error) {
	f.operation, f.actor, f.id, f.mode, f.offset, f.take = "replies", a, id, mode, o, n
	if mode == "featured" {
		return []Post{{ID: id}}, 1, nil
	}
	return []Post{}, 6, nil
}

func TestCompletionRoutesBindIdentityPermissionAndPagination(t *testing.T) {
	f := &completionFake{}
	a := &fakeAuth{actor: testActor}
	h := NewHandler(f, a)
	w := request(h, "POST", "/api/posts/"+testActor+"/boost", `{"content":"note"}`)
	if w.Code != 200 || f.operation != "boost" || f.actor != testActor || f.content == nil || *f.content != "note" || a.permission != "posts.boost" {
		t.Fatal(w.Code, w.Body.String(), f, a.permission)
	}
	w = request(h, "DELETE", "/api/posts/"+testActor+"/boost", "")
	if w.Code != 204 || f.operation != "unboost" || a.permission != "posts.boost" {
		t.Fatal(w.Code, f.operation, a.permission)
	}
	w = request(h, "GET", "/api/posts/"+testActor+"/boosts?offset=2&take=7", "")
	if w.Code != 200 || w.Header().Get("X-Total") != "3" || f.offset != 2 || f.take != 7 {
		t.Fatal(w.Code, w.Header(), f)
	}
	w = request(h, "GET", "/api/posts/reactions/users/alice?take=9&order=created", "")
	if w.Code != 200 || f.operation != "reaction-user" || f.name != "alice" || w.Header().Get("X-Total") != "5" {
		t.Fatal(w.Code, w.Body.String(), f)
	}
	w = request(h, "GET", "/api/posts/"+testActor+"/replies/featured", "")
	if w.Code != 200 || f.mode != "featured" {
		t.Fatal(w.Code, w.Body.String(), f.mode)
	}
	w = request(h, "GET", "/api/posts/"+testActor+"/replies?orderDesc=false", "")
	if w.Code != 200 || f.mode != "list" {
		t.Fatal(w.Code, w.Body.String(), f.mode)
	}
}

func TestCompletionRoutesFailClosedBeforeStorage(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/posts/" + testActor + "/boost?x=1", `{"content":"x"}`},
		{"POST", "/api/posts/" + testActor + "/boost", `{"content":"x","publisher_id":"forged"}`},
		{"DELETE", "/api/posts/" + testActor + "/boost", `{}`},
		{"GET", "/api/posts/featured?take=101", ""},
		{"GET", "/api/posts/reactions/users/alice?order=secret", ""},
		{"GET", "/api/posts/" + testActor + "/replies?take=2&take=3", ""},
	} {
		f := &completionFake{}
		w := request(NewHandler(f, &fakeAuth{actor: testActor}), tc.method, tc.path, tc.body)
		if w.Code < 400 || f.operation != "" {
			t.Fatal(tc, w.Code, f.operation)
		}
	}
	f := &completionFake{}
	w := request(NewHandler(f, &fakeAuth{}), "POST", "/api/posts/"+testActor+"/boost", `null`)
	if w.Code != 401 || f.operation != "" {
		t.Fatal(w.Code, f.operation)
	}
}
