// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type pinFake struct {
	fakeRepo
	mode      *int
	actor, id string
}

func (p *pinFake) Pin(_ context.Context, a, id string, mode int) (Post, error) {
	p.calls++
	p.actor = a
	p.id = id
	p.mode = &mode
	return Post{ID: id, PinMode: p.mode}, nil
}
func (p *pinFake) Unpin(_ context.Context, a, id string) (Post, error) {
	p.calls++
	p.actor = a
	p.id = id
	p.mode = nil
	return Post{ID: id}, nil
}
func TestPinHTTPActiveModesAndUnpin(t *testing.T) {
	p := &pinFake{}
	a := &fakeAuth{actor: testActor}
	h := NewHandler(p, a)
	for _, body := range []string{`{"mode":0}`, `{"mode":1}`, `{"mode":2}`} {
		w := request(h, "POST", "/api/posts/"+testActor+"/pin", body)
		var out Post
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil || w.Code != 200 || out.PinMode == nil || *out.PinMode != *p.mode || a.permission != "posts.pin" || p.actor != testActor {
			t.Fatal(w.Code, w.Body.String(), e)
		}
	}
	w := request(h, "DELETE", "/api/posts/"+testActor+"/pin", "")
	if w.Code != 200 || p.mode != nil || p.calls != 4 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPinHTTPRejectsAmbiguityAndUnsupportedReplyBeforeStorage(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `[]`, `{"mode":null}`, `{"mode":"0"}`, `{"mode":0.0}`, `{"mode":0,"mode":1}`, `{"mode":-1}`, `{"mode":3}`, `{"mode":0,"account_id":"x"}`, `{"mode":0} {}`, `{"Mode":0}`, `{"mode":true}`} {
		p := &pinFake{}
		w := request(NewHandler(p, &fakeAuth{actor: testActor}), "POST", "/api/posts/"+testActor+"/pin", body)
		if (w.Code != 400 && w.Code != 501) || p.calls != 0 {
			t.Fatal(body, w.Code, p.calls)
		}
	}
	for _, tc := range []struct{ method, path, body string }{{"POST", "/api/posts/bad/pin", `{"mode":0}`}, {"POST", "/api/posts/" + testActor + "/pin?mode=0", `{"mode":0}`}, {"DELETE", "/api/posts/" + testActor + "/pin", `{}`}} {
		p := &pinFake{}
		w := request(NewHandler(p, &fakeAuth{actor: testActor}), tc.method, tc.path, tc.body)
		if w.Code < 400 || p.calls != 0 {
			t.Fatal(tc, w.Code)
		}
	}
}
func TestPinHTTPRequiresCurrentIdentityAndPermission(t *testing.T) {
	for _, a := range []*fakeAuth{{}, {actor: "forged"}, {actor: testActor, authenticate: ErrUnauthenticated}, {actor: testActor, authorize: ErrForbidden}, {actor: testActor, authorize: ErrUnavailable}} {
		for _, method := range []string{"POST", "DELETE"} {
			p := &pinFake{}
			w := request(NewHandler(p, a), method, "/api/posts/"+testActor+"/pin", `{"mode":0}`)
			if w.Code < 400 || p.calls != 0 {
				t.Fatal(w.Code, p.calls)
			}
		}
	}
	w := request(NewHandler(&fakeRepo{}, &fakeAuth{actor: testActor}), "POST", "/api/posts/"+testActor+"/pin", `{"mode":0}`)
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
}
func TestPinnedListingScopeAndTriState(t *testing.T) {
	for _, tc := range []struct {
		q    string
		want *bool
		code int
	}{{"", nil, 200}, {"?pinned=false", pinBool(false), 200}, {"?pinned=true&pub=alice", pinBool(true), 200}, {"?pinned=true&realm=group", pinBool(true), 200}, {"?pinned=true", nil, 400}, {"?pinned=true&pinned=false&pub=alice", nil, 400}, {"?pinned=maybe&pub=alice", nil, 400}} {
		p := &fakeRepo{}
		w := request(NewHandler(p, &fakeAuth{}), http.MethodGet, "/api/posts"+tc.q, "")
		if w.Code != tc.code || w.Code == 200 && (p.options.Pinned == nil) != (tc.want == nil) || tc.want != nil && p.options.Pinned != nil && *tc.want != *p.options.Pinned {
			t.Fatal(tc, w.Code, p.options)
		}
	}
}
func TestValidatePinRejectsOnlyUnsupportedSourceShapes(t *testing.T) {
	id := testActor
	for _, p := range []Post{{RepliedPostID: &id}, {ChainedPostID: &id}} {
		for _, mode := range []int{PublisherPin, RealmPin} {
			p.RealmID = &id
			if !errors.Is(ValidatePin(p, mode), ErrPinFailed) {
				t.Fatal(p, mode)
			}
		}
	}
	if ValidatePin(Post{}, PublisherPin) != nil || ValidatePin(Post{RealmID: &id}, RealmPin) != nil || ValidatePin(Post{RepliedPostID: &id}, ReplyPin) != nil || !errors.Is(ValidatePin(Post{}, RealmPin), ErrPinFailed) || !errors.Is(ValidatePin(Post{}, ReplyPin), ErrPinFailed) {
		t.Fatal("mode policy")
	}
}

func pinBool(v bool) *bool { return &v }
