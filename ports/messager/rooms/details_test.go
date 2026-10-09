// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
	"time"
)

type detailRepo struct {
	fakeRepo
	calls       int
	nick        *string
	scope, slug string
	timeout     ModerationTimeout
}

func (r *detailRepo) PublicBySlug(_ context.Context, a, scope, slug string) (Room, error) {
	r.calls++
	r.actor = a
	r.scope = scope
	r.slug = slug
	return Room{ID: testRoom}, nil
}
func (r *detailRepo) OnlineMembers(_ context.Context, a, rid string) (OnlineMembers, error) {
	r.calls++
	r.actor = a
	return OnlineMembers{OnlineUserNames: []string{}}, nil
}
func (r *detailRepo) UpdateProfile(_ context.Context, a, rid string, q ProfileRequest) (Member, error) {
	r.calls++
	r.actor = a
	r.nick = q.Nick
	return Member{Nick: q.Nick}, nil
}
func (r *detailRepo) ClearProfile(_ context.Context, a, rid string) (Member, error) {
	r.calls++
	r.actor = a
	r.nick = nil
	return Member{}, nil
}
func (r *detailRepo) ModerateDelete(_ context.Context, a, rid, mid string, q ModerationDelete) error {
	r.calls++
	r.actor = a
	return nil
}
func (r *detailRepo) ModerateTimeout(_ context.Context, a, rid, target string, q ModerationTimeout) (ModerationTimeoutResult, error) {
	r.calls++
	r.actor = a
	r.timeout = q
	return ModerationTimeoutResult{Success: true, TimeoutUntil: time.Unix(100, 0)}, nil
}
func TestRoomDetailsAuthoredMethodsAndStrictBodies(t *testing.T) {
	a := &fakeAuthority{}
	repo := &detailRepo{}
	h := (&Service{Repository: repo, Authority: a}).Handler()
	profile := "/api/chat/" + testRoom + "/members/me/profile"
	w := call(h, "PATCH", profile, `{"nick":"Room name"}`, "Bearer fixture")
	if w.Code != 200 || a.permission != "chat.members.manage" || repo.nick == nil || *repo.nick != "Room name" {
		t.Fatal(w, a, repo)
	}
	a.permission = ""
	w = call(h, "DELETE", profile, `{}`, "Bearer fixture")
	if w.Code != 200 || a.permission != "" || !strings.Contains(w.Body.String(), `"nick":null`) {
		t.Fatal(w, a)
	}
	for _, body := range []string{`{"nick":"one","nick":"two"}`, `{"Nick":"one"}`, `{"account_id":"other"}`, `{"nick":42}`, `null`, `{"nick":"` + strings.Repeat("😀", 513) + `"}`} {
		before := repo.calls
		if w = call(h, "PATCH", profile, body, "Bearer fixture"); w.Code != 400 || repo.calls != before {
			t.Fatal(body, w)
		}
	}
	for _, body := range []string{`{"picture_id":"https://example.invalid/image"}`, `{"background_id":"arbitrary"}`} {
		before := repo.calls
		if w = call(h, "PATCH", profile, body, "Bearer fixture"); w.Code != 501 || repo.calls != before {
			t.Fatal(body, w)
		}
	}
	timeout := "/api/chat/rooms/" + testRoom + "/members/" + testActor + "/timeout"
	w = call(h, "POST", timeout, `{"duration_minutes":5,"reason":"Test"}`, "Bearer fixture")
	if w.Code != 200 || a.permission != "chat.members.timeout" || repo.timeout.DurationMinutes != 5 {
		t.Fatal(w, a)
	}
	for _, body := range []string{`{}`, `{"duration_minutes":0}`, `{"duration_minutes":-1}`, `{"duration_minutes":43201}`, `{"duration_minutes":2,"timeout_until":"2026-01-01"}`} {
		before := repo.calls
		w = call(h, "POST", timeout, body, "Bearer fixture")
		if w.Code != 400 || repo.calls != before {
			t.Fatal(body, w)
		}
	}
	w = call(h, "DELETE", "/api/chat/rooms/"+testRoom+"/messages/"+testActor, `{"reason":"Test"}`, "Bearer fixture")
	if w.Code != 204 || a.permission != "chat.messages.delete" {
		t.Fatal(w, a)
	}
	w = call(h, "POST", profile, `{}`, "Bearer fixture")
	if w.Code != 405 || w.Header().Get("Allow") != "PATCH, DELETE" {
		t.Fatal(w)
	}
}

type detailBoundAuthority struct {
	fakeAuthority
	bound   bool
	initial error
}

func (a *detailBoundAuthority) RequestIdentityPresent(*http.Request) bool { return a.bound }
func (a *detailBoundAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if a.initial != nil {
		return authority.Principal{}, a.initial
	}
	return a.fakeAuthority.AuthenticateRequest(r)
}
func TestRoomDetailAnonymousDoesNotDowngradeRejectedProof(t *testing.T) {
	a := &detailBoundAuthority{}
	repo := &detailRepo{}
	h := (&Service{Repository: repo, Authority: a}).Handler()
	for _, path := range []string{"/api/chat/public/owner/slug", "/api/chat/" + testRoom + "/members/online"} {
		w := call(h, "GET", path, "", "")
		if w.Code != 200 || repo.actor != "" {
			t.Fatal(w, repo)
		}
		before := repo.calls
		w = call(h, "GET", path, "", "Bearer malformed")
		if w.Code != 401 || repo.calls != before {
			t.Fatal(w)
		}
		a.bound = true
		a.initial = authority.ErrInvalid
		w = call(h, "GET", path, "", "")
		if w.Code != 401 || repo.calls != before {
			t.Fatal("bound invalid proof downgraded", w)
		}
		a.bound = false
		a.initial = nil
		a.denied = authority.ErrRevoked
		w = call(h, "GET", path, "", "Bearer fixture")
		if w.Code != 401 || repo.calls != before {
			t.Fatal(w)
		}
		a.denied = nil
	}
}
func TestRoomSlugNormalizationAndClearing(t *testing.T) {
	for _, v := range []struct {
		raw, normalized string
		valid           bool
	}{{"  HELLO.World_2  ", "hello.world_2", true}, {"", "", true}, {"   ", "", true}, {"x", "x", true}, {".x", "", false}, {"x_", "", false}, {"a/b", "", false}, {"é", "", false}, {strings.Repeat("a", 129), "", false}} {
		got, e := NormalizeSlug(&v.raw)
		if (e == nil) != v.valid {
			t.Fatal(v, got, e)
		}
		if e == nil && ((got == nil && v.normalized != "") || (got != nil && *got != v.normalized)) {
			t.Fatal(v, got)
		}
	}
}

func TestRoomPublicSlugLiteralDoesNotCollideWithOnlineSuffix(t *testing.T) {
	repo := &detailRepo{}
	h := (&Service{Repository: repo, Authority: &fakeAuthority{}}).Handler()
	w := call(h, "GET", "/api/chat/public/members/online", "", "")
	if w.Code != 200 || repo.calls != 1 || repo.scope != "members" || repo.slug != "online" {
		t.Fatal(w, repo)
	}
}
