// SPDX-License-Identifier: AGPL-3.0-only
package interactions

import (
	"context"
	"encoding/json"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
)

type collectionFake struct {
	Repository
	calls                          int
	actor, publisher, slug, anchor string
	next, created                  bool
	err                            error
}

func (f *collectionFake) record(a, p, s string) { f.calls++; f.actor = a; f.publisher = p; f.slug = s }
func (f *collectionFake) SubscribeCollection(_ context.Context, a, p, s string) (CollectionSubscription, bool, error) {
	f.record(a, p, s)
	c := "10000000-0000-4000-8000-000000000003"
	return CollectionSubscription{ID: actorID, AccountID: a, CollectionID: &c}, f.created, f.err
}
func (f *collectionFake) UnsubscribeCollection(_ context.Context, a, p, s string) error {
	f.record(a, p, s)
	return f.err
}
func (f *collectionFake) CollectionSubscription(_ context.Context, a, p, s string) (CollectionSubscription, error) {
	v, _, e := f.SubscribeCollection(context.Background(), a, p, s)
	return v, e
}
func (f *collectionFake) AdjacentCollectionPost(_ context.Context, a, p, s, id string, next bool) (posts.Post, error) {
	f.record(a, p, s)
	f.anchor = id
	f.next = next
	return posts.Post{ID: id}, f.err
}
func TestCollectionSubscriptionAuthoredHTTP(t *testing.T) {
	f := &collectionFake{created: true}
	a := &fakeAuth{actor: actorID}
	h := NewHandler(f, a)
	root := "/api/publishers/SomeName/collections/series"
	w := call(h, "POST", root+"/subscribe", "")
	var sub CollectionSubscription
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &sub) != nil || sub.AccountID != actorID || sub.CollectionID == nil || sub.TagID != nil || sub.CategoryID != nil || w.Header().Get("Location") != root+"/subscription" || a.permission != "post.collections.posts.manage" || f.publisher != "SomeName" {
		t.Fatal(w.Code, w.Body.String(), w.Header(), f, a)
	}
	f.created = false
	if w = call(h, "POST", root+"/subscribe", ""); w.Code != 200 || w.Header().Get("Location") != "" {
		t.Fatal(w.Code, w.Header())
	}
	for range 2 {
		if w = call(h, "POST", root+"/unsubscribe", ""); w.Code != 204 || w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	a.permission = ""
	if w = call(h, "GET", root+"/subscription", ""); w.Code != 200 || a.permission != "" {
		t.Fatal(w.Code, a)
	}
	f.err = ErrCollectionSubscriptionNotFound
	if w = call(h, "GET", root+"/subscription", ""); w.Code != 404 || !strings.Contains(w.Body.String(), "COLLECTION_SUBSCRIPTION_NOT_FOUND") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestCollectionSubscriptionStrictBinding(t *testing.T) {
	root := "/api/publishers/someone/collections/series"
	for _, v := range []struct {
		method, path, body, actor string
		code                      int
	}{
		{"POST", root + "/subscribe", "", "", 401}, {"POST", root + "/unsubscribe", "", "", 401}, {"GET", root + "/subscription", "", "", 401},
		{"POST", root + "/subscribe?account_id=" + actorID, "", actorID, 501}, {"POST", root + "/subscribe", `{"account_id":"other"}`, actorID, 400}, {"GET", root + "/subscription", `{}`, actorID, 400},
		{"DELETE", root + "/subscribe", "", actorID, 405}, {"GET", root + "/posts/invalid/next", "", actorID, 404}, {"GET", root + "/posts/" + actorID + "/prev?offset=1", "", actorID, 501},
	} {
		f := &collectionFake{}
		w := call(NewHandler(f, &fakeAuth{actor: v.actor}), v.method, v.path, v.body)
		if w.Code != v.code || f.calls != 0 {
			t.Fatal(v, w.Code, w.Body.String(), f.calls)
		}
	}
}
func TestCollectionNavigationDirectionAndErrors(t *testing.T) {
	for _, next := range []bool{false, true} {
		direction := "prev"
		missing := ErrPrevCollectionPost
		if next {
			direction = "next"
			missing = ErrNextCollectionPost
		}
		f := &collectionFake{}
		h := NewHandler(f, &fakeAuth{})
		root := "/api/publishers/someone/collections/series/posts/" + actorID + "/" + direction
		w := call(h, "GET", root, "")
		if w.Code != 200 || f.anchor != actorID || f.next != next || f.actor != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, f, w.Header())
		}
		f.err = missing
		if w = call(h, "GET", root, ""); w.Code != 404 || !strings.Contains(w.Body.String(), missing.Message) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
