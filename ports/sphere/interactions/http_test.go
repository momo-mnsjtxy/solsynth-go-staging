// SPDX-License-Identifier: AGPL-3.0-only
package interactions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/sphere/posts"
)

const actorID = "10000000-0000-4000-8000-000000000001"

type fakeAuth struct {
	actor, permission string
	err               error
}

func (a *fakeAuth) Authenticate(*http.Request) (string, error) { return a.actor, a.err }
func (a *fakeAuth) Authorize(_ *http.Request, p string) error  { a.permission = p; return a.err }

type fakeRepo struct {
	Repository
	called  int
	removed bool
	v       ReactionRequest
}

func (r *fakeRepo) React(_ context.Context, actor, id string, v ReactionRequest) (Reaction, bool, error) {
	r.called++
	r.v = v
	return Reaction{ID: actorID, PostID: id, AccountID: actor, Symbol: v.Symbol, Attitude: v.Attitude}, r.removed, nil
}
func (r *fakeRepo) BookmarkStatus(context.Context, string, string) (*Bookmark, error) {
	r.called++
	return nil, nil
}
func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestReactionBindingAndToggle(t *testing.T) {
	r := &fakeRepo{}
	a := &fakeAuth{actor: actorID}
	h := NewHandler(r, a)
	w := call(h, "POST", "/api/posts/"+actorID+"/reactions", `{"symbol":"heart","attitude":0}`)
	if w.Code != 200 || r.called != 1 || a.permission != "posts.react" || r.v.Attitude != Positive {
		t.Fatal(w.Code, w.Body.String(), r, a)
	}
	r.removed = true
	w = call(h, "POST", "/api/posts/"+actorID+"/reactions", `{"symbol":"heart","attitude":0}`)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestBoundariesRejectBeforeMutation(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"symbol":"heart","symbol":"laugh"}`, `{"symbol":"heart","account_id":"other"}`, `{"symbol":"heart","attitude":"0"}`, `{"symbol":"heart"} {}`, `{"symbol":"heart","Symbol":"cry"}`} {
		r := &fakeRepo{}
		w := call(NewHandler(r, &fakeAuth{actor: actorID}), "POST", "/api/posts/"+actorID+"/reactions", body)
		if w.Code != 400 || r.called != 0 {
			t.Fatal(body, w.Code, r.called)
		}
	}
	for _, a := range []*fakeAuth{{}, {actor: "bad"}, {actor: actorID, err: posts.ErrUnauthenticated}} {
		r := &fakeRepo{}
		w := call(NewHandler(r, a), "POST", "/api/posts/"+actorID+"/reactions", `{"symbol":"heart"}`)
		if w.Code != 401 || r.called != 0 {
			t.Fatal(w.Code, r.called)
		}
	}
	if w := call(NewHandler(nil, nil), "GET", "/api/collections", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
}
func TestBookmarkNullAndPrivacyHeaders(t *testing.T) {
	r := &fakeRepo{}
	w := call(NewHandler(r, &fakeAuth{actor: actorID}), "GET", "/api/posts/"+actorID+"/bookmark", "")
	var v any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v != nil || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "Authorization" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}
func TestValidationAndPaging(t *testing.T) {
	for _, v := range []ReactionRequest{{Symbol: "heart", Attitude: -1}, {Symbol: "heart", Attitude: 3}, {Symbol: "", Attitude: 0}} {
		if !errors.Is(ValidateReaction(v), ErrInvalid) {
			t.Fatal(v)
		}
	}
	if !errors.Is(ValidateReaction(ReactionRequest{Symbol: "custom+pack", Attitude: 1}), ErrUnsupported) {
		t.Fatal("custom reaction was invented")
	}
	for _, q := range []string{"take=0", "take=101", "offset=-1", "take=1&take=2", "actor=x", "offset=x"} {
		if _, e := page(httptest.NewRequest("GET", "/api/collections?"+q, nil)); e == nil {
			t.Fatal(q)
		}
	}
	long := strings.Repeat("😀", 129)
	if e := ValidateCollection(CollectionRequest{Slug: "x", Name: &long}, true); !errors.Is(e, ErrInvalid) {
		t.Fatal("UTF-16 collection bound", e)
	}
}
