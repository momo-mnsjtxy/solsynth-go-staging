// SPDX-License-Identifier: AGPL-3.0-only
package timeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
	"time"
)

const actorID = "10000000-0000-4000-8000-000000000001"

type fakeAuth struct {
	actor      string
	permission string
	deny       bool
}

func (a *fakeAuth) Authenticate(*http.Request) (string, error) { return a.actor, nil }
func (a *fakeAuth) Authorize(_ *http.Request, p string) error {
	a.permission = p
	if a.deny {
		return posts.ErrForbidden
	}
	return nil
}

type recorderRepo struct {
	Repository
	calls     int
	options   Options
	search    SearchOptions
	kind, ref string
	updates   []sphere.WatchPreferenceUpdate
}

func (r *recorderRepo) Feed(_ context.Context, a string, o Options) (Page, error) {
	r.calls++
	r.options = o
	return Page{Items: []Event{}, Mode: o.Mode}, nil
}
func (r *recorderRepo) Search(_ context.Context, a string, o SearchOptions) ([]posts.Post, int64, error) {
	r.calls++
	r.search = o
	return []posts.Post{}, 7, nil
}
func (r *recorderRepo) Unsuppress(_ context.Context, a, k, id string) error {
	r.calls++
	r.kind = k
	r.ref = id
	return nil
}
func (r *recorderRepo) PatchWatch(_ context.Context, a string, v []sphere.WatchPreferenceUpdate) ([]sphere.WatchPreference, error) {
	r.calls++
	r.updates = v
	return []sphere.WatchPreference{sphere.DefaultWatchPreference(a, sphere.WatchBookmark)}, nil
}
func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func TestHTTPSourceRoutesAndAuth(t *testing.T) {
	repo := &recorderRepo{}
	auth := &fakeAuth{actor: actorID}
	h := NewHandler(repo, auth)
	w := call(h, "GET", "/api/timeline?mode=TOP&filter=subscriptions&take=12&aggressive=false", "")
	if w.Code != 200 || repo.options.Mode != sphere.Top || repo.options.Take != 12 || repo.options.Aggressive {
		t.Fatal(w.Code, w.Body.String(), repo.options)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "Authorization" {
		t.Fatal("personalized response cache")
	}
	w = call(h, "GET", "/api/posts?query=sample&tags=one&tags=two&replies=false&periodStart=1700000000&orderDesc=false", "")
	if w.Code != 200 || w.Header().Get("X-Total") != "7" || len(repo.search.Tags) != 2 || repo.search.Replies == nil || *repo.search.Replies || !repo.search.Ascending {
		t.Fatal(w.Code, w.Body.String(), repo.search)
	}
	for _, path := range []string{"/api/posts?take=0", "/api/posts?take=2&take=3", "/api/posts?shuffle=true", "/api/posts?media=true", "/api/posts?searchEngine=semantic", "/api/posts?realm=unknown"} {
		w = call(h, "GET", path, "")
		if w.Code != 400 && w.Code != 501 {
			t.Fatal(path, w.Code)
		}
	}
	auth.actor = ""
	w = call(h, "PUT", "/api/posts/watch", `{"bookmark":{"replies":false}}`)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	auth.actor = actorID
	auth.deny = true
	w = call(h, "PUT", "/api/posts/watch", `{}`)
	if w.Code != 403 || auth.permission != "post.subscriptions.manage" {
		t.Fatal(w.Code, auth.permission)
	}
}
func TestHTTPWatchStrictNestedPatches(t *testing.T) {
	repo := &recorderRepo{}
	auth := &fakeAuth{actor: actorID}
	h := NewHandler(repo, auth)
	for _, body := range []string{`null`, `[]`, `{"bookmark":{"replies":false,"replies":true}}`, `{"Bookmark":{},"bookmark":{}}`, `{"bookmark":{"account_id":"spoof"}}`, `{"account_id":"spoof"}`, `{} {}`} {
		if w := call(h, "PUT", "/api/posts/watch", body); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if repo.calls != 0 {
		t.Fatal("invalid body reached repository")
	}
	w := call(h, "PUT", "/api/posts/watch", `{"bookmark":{"replies":false},"reaction":{"edits":true}}`)
	if w.Code != 200 || len(repo.updates) != 2 || repo.updates[0].NotifyReplies == nil || *repo.updates[0].NotifyReplies || repo.updates[0].NotifyEdits != nil {
		t.Fatal(w.Code, w.Body.String(), repo.updates)
	}
}
func TestHTTPUninterestedControllerAndAuthoredCaller(t *testing.T) {
	repo := &recorderRepo{}
	auth := &fakeAuth{actor: actorID}
	h := NewHandler(repo, auth)
	w := call(h, "DELETE", "/api/timeline/discovery/uninterested?kind=publisher&referenceId="+actorID, "")
	if w.Code != 200 || repo.kind != "publisher" || repo.ref != actorID || auth.permission != "timelines.feedback" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(h, "DELETE", "/api/timeline/discovery/uninterested", `{"kind":"publisher","reference_id":"`+actorID+`"}`)
	if w.Code != 200 || repo.ref != actorID {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(h, "DELETE", "/api/timeline/discovery/uninterested?kind=publisher&referenceId="+actorID, `{"kind":"account","reference_id":"`+actorID+`"}`)
	if w.Code != 400 {
		t.Fatal("ambiguous query/body", w.Code)
	}
}
func TestEventAndSubscriptionAuthoredEnvelope(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := posts.Post{ID: actorID}
	event := Event{ID: p.ID, Type: "posts.new", ResourceIdentifier: p.ID, Data: &p, CreatedAt: now, UpdatedAt: now}
	b, e := json.Marshal(event)
	if e != nil {
		t.Fatal(e)
	}
	var got map[string]any
	if e = json.Unmarshal(b, &got); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"id", "type", "resource_identifier", "data", "created_at", "updated_at", "deleted_at"} {
		if _, ok := got[key]; !ok {
			t.Fatal("authored event key missing", key)
		}
	}
	b, e = json.Marshal(SubscriptionItem{Subscription: Subscription{ID: actorID, State: "accepted"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &got); e != nil {
		t.Fatal(e)
	}
	if _, ok := got["subscription"].(map[string]any); !ok {
		t.Fatal("subscription envelope flattened", string(b))
	}
}
func TestOwnedRouteDispatch(t *testing.T) {
	for _, p := range []string{"/api/timeline", "/api/timeline/discovery/profile", "/api/posts", "/api/posts/watch", "/api/posts/" + actorID + "/thread", "/api/posts/" + actorID + "/replies/threaded", "/api/publishers/alice/subscription/requests"} {
		if !Handles(httptest.NewRequest("GET", p, nil)) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/api/posts/" + actorID, "/api/posts/" + actorID + "/reactions", "/api/publishers/alice/collections"} {
		if Handles(httptest.NewRequest("GET", p, nil)) {
			t.Fatal("stole canonical route", p)
		}
	}
	if Handles(httptest.NewRequest("POST", "/api/posts", nil)) {
		t.Fatal("stole canonical create")
	}
}
