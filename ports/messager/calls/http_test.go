// SPDX-License-Identifier: AGPL-3.0-only
package calls

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

const actorID = "ca000000-0000-4000-8000-000000000001"
const roomID = "ca000000-0000-4000-8000-000000000002"
const targetID = "ca000000-0000-4000-8000-000000000003"

type testAuthority struct {
	permissions []string
	fail        error
}

func (a *testAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer synthetic" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: actorID}}, a.fail
}
func (a *testAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, a.fail
}
func (a *testAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, permissions ...string) (authority.Principal, error) {
	a.permissions = append(a.permissions, permissions...)
	return p, a.fail
}

type testRepository struct {
	last, actor, room, target, tool string
	err                             error
	count                           int
}

func (r *testRepository) record(method, actor, room string) error {
	r.last = method
	r.actor = actor
	r.room = room
	r.count++
	return r.err
}
func (r *testRepository) Start(_ context.Context, a, b string) (Call, error) {
	return Call{ID: roomID, State: "active"}, r.record("start", a, b)
}
func (r *testRepository) Get(_ context.Context, a, b string) (Call, error) {
	return Call{ID: roomID, State: "active"}, r.record("get", a, b)
}
func (r *testRepository) Join(_ context.Context, a, b, tool string) (JoinResponse, error) {
	r.tool = tool
	return JoinResponse{Token: "synthetic", Participants: []Participant{}}, r.record("join", a, b)
}
func (r *testRepository) Participants(_ context.Context, a, b string) ([]Participant, error) {
	return []Participant{}, r.record("participants", a, b)
}
func (r *testRepository) Leave(_ context.Context, a, b, tool string) error {
	r.tool = tool
	return r.record("leave", a, b)
}
func (r *testRepository) End(_ context.Context, a, b string) error { return r.record("end", a, b) }
func (r *testRepository) Kick(_ context.Context, a, b, target string, k KickRequest) error {
	r.target = target
	return r.record("kick", a, b)
}
func (r *testRepository) Mute(_ context.Context, a, b, target string, muted bool) error {
	r.target = target
	method := "mute"
	if !muted {
		method = "unmute"
	}
	return r.record(method, a, b)
}
func (r *testRepository) Invite(_ context.Context, a, b, target string) error {
	r.target = target
	return r.record("invite", a, b)
}
func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestSourceCallRoutesAndOwnedEndDispatch(t *testing.T) {
	repo := &testRepository{}
	auth := &testAuthority{}
	h := (&Service{Repository: repo, Authority: auth}).Handler()
	base := "/api/chat/realtime/" + roomID
	for _, tc := range []struct {
		method, suffix, operation string
		status                    int
	}{{"GET", "", "get", 200}, {"POST", "", "start", 200}, {"GET", "/join?tool=screen_share", "join", 200}, {"GET", "/participants", "participants", 200}, {"DELETE", "?tool=screen_share", "leave", 204}, {"POST", "/kick/" + targetID, "kick", 204}, {"POST", "/mute/" + targetID, "mute", 204}, {"POST", "/unmute/" + targetID, "unmute", 204}, {"POST", "/invite/" + targetID, "invite", 204}, {"POST", "/end", "end", 204}} {
		w := request(h, tc.method, base+tc.suffix, "{}")
		if w.Code != tc.status || repo.last != tc.operation || repo.actor != actorID || repo.room != roomID || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(tc, w.Code, w.Body.String(), repo)
		}
	}
	if repo.tool != "screen_share" || repo.target != targetID || strings.Join(auth.permissions, ",") != "chat.call.kick,chat.call.mute,chat.call.mute,chat.call.invite" {
		t.Fatal(repo, auth.permissions)
	}
}
func TestCallHTTPRejectsForgedIdentityAndAmbiguousInput(t *testing.T) {
	repo := &testRepository{}
	h := (&Service{Repository: repo, Authority: &testAuthority{}}).Handler()
	base := "/api/chat/realtime/" + roomID
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{{"GET", base + "/join?token=x", "", 400}, {"GET", base + "/join?tool=x&tool=y", "", 400}, {"GET", base + "/join?tool=bad%2Ftool", "", 400}, {"GET", base + "/participants?tool=x", "", 400}, {"POST", base, `{"provider":"untrusted"}`, 400}, {"POST", base + "/kick/" + targetID, `{"reason":"x","reason":"y"}`, 400}, {"POST", base + "/kick/" + targetID, `{"reason":"x"} {}`, 400}, {"HEAD", base, "", 405}, {"GET", "/api/chat/realtime/not-a-uuid", "", 400}, {"GET", base + "//join", "", 400}} {
		w := request(h, tc.method, tc.path, tc.body)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	for _, header := range []string{"X-Account-ID", "X-User-ID", "X-Role", "X-Test-Actor", "X-Synthetic-Actor", "X-App-Secret", "Proxy-Authorization"} {
		r := httptest.NewRequest("GET", base, nil)
		r.Header.Set("Authorization", "Bearer synthetic")
		r.Header.Set(header, "forged")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(header, w.Code)
		}
	}
	if repo.count != 0 {
		t.Fatal("invalid request reached repository", repo.count)
	}
}
func TestCallHTTPFailuresDoNotBecomeSuccessfulResponses(t *testing.T) {
	repo := &testRepository{err: ErrProvider}
	h := (&Service{Repository: repo, Authority: &testAuthority{}}).Handler()
	base := "/api/chat/realtime/" + roomID
	w := request(h, "DELETE", base, "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "CHAT_CALL_PROVIDER_UNAVAILABLE") {
		t.Fatal(w.Code, w.Body.String())
	}
	repo.err = errors.New("private provider diagnostic")
	w = request(h, "POST", base, "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	h = (&Service{Repository: repo, Authority: &testAuthority{fail: authority.ErrRevoked}}).Handler()
	w = request(h, "GET", base, "")
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatal(w.Code, w.Body.String())
	}
}
