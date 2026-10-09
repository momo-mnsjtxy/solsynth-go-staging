// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"strings"
	"testing"
)

const testActor = "c1000000-0000-4000-8000-000000000001"
const testRoom = "c2000000-0000-4000-8000-000000000001"

type fakeAuthority struct {
	denied     error
	fresh      int
	permission string
}

func (a *fakeAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer fixture" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: testActor}}, nil
}
func (a *fakeAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	a.fresh++
	return p, a.denied
}
func (a *fakeAuthority) AuthorizePrincipal(c context.Context, p authority.Principal, v ...string) (authority.Principal, error) {
	a.permission = strings.Join(v, ",")
	return a.AuthenticatePrincipal(c, p)
}

type fakeRepo struct {
	Repository
	writes      int
	actor, room string
	action      MemberAction
	request     TextRequest
}

func (r *fakeRepo) Create(_ context.Context, a string, p RoomPatch) (Room, error) {
	r.writes++
	r.actor = a
	return Room{ID: testRoom, AccountID: a, Name: *p.Name}, nil
}
func (r *fakeRepo) Send(_ context.Context, a, rid string, p TextRequest) (messager.MessageProjection, error) {
	r.writes++
	r.actor = a
	r.room = rid
	r.request = p
	return messager.MessageProjection{ID: testRoom, Content: &p.Content}, nil
}
func (r *fakeRepo) ChangeMember(_ context.Context, a, rid string, action MemberAction, target string) (Member, error) {
	r.writes++
	r.actor = a
	r.room = rid
	r.action = action
	return Member{AccountID: a, ChatRoomID: rid}, nil
}
func call(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestOwnedIdentityAndPermissions(t *testing.T) {
	a := &fakeAuthority{}
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: a}).Handler()
	if w := call(h, "POST", "/api/chat", `{"name":"room"}`, ""); w.Code != 401 || repo.writes != 0 {
		t.Fatal(w, repo)
	}
	w := call(h, "POST", "/api/chat", `{"name":"room"}`, "Bearer fixture")
	if w.Code != 200 || a.permission != "chat.create" || a.fresh != 1 || repo.actor != testActor {
		t.Fatal(w, a, repo)
	}
	a.denied = authority.ErrRevoked
	w = call(h, "POST", "/api/chat", `{"name":"room"}`, "Bearer fixture")
	if w.Code != 401 || repo.writes != 1 {
		t.Fatal(w, repo)
	}
	a.denied = nil
	r := httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"name":"room"}`))
	r.Header.Set("Authorization", "Bearer fixture")
	r.Header.Set("X-Account-ID", testActor)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || repo.writes != 1 {
		t.Fatal(w, repo)
	}
}
func TestStrictTextHTTPRejectsUnsupportedAndAmbiguousInput(t *testing.T) {
	a := &fakeAuthority{}
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: a}).Handler()
	target := "/api/chat/" + testRoom + "/messages"
	for _, v := range []struct {
		body   string
		status int
	}{{`{"content":"one","content":"two"}`, 400}, {`{"Content":"one"}`, 400}, {`null`, 400}, {`[]`, 400}, {`{"content":"one"} {}`, 400}, {`{"content":"one","sender_id":"x"}`, 400}, {`{"content":"one","encryption_meta":{}}`, 501}, {`{"content":"one","thread_id":"x"}`, 400}, {`{"content":"one","forwarded_message_id":"x"}`, 400}} {
		w := call(h, "POST", target, v.body, "Bearer fixture")
		if w.Code != v.status || repo.writes != 0 {
			t.Fatal(v, w, repo)
		}
	}
	for _, q := range []struct {
		suffix string
		status int
	}{{"?identity=" + testActor, 501}, {"?access_token=x", 400}, {"?take=1&take=2", 400}, {"?token=x", 400}, {"?bogus=1", 400}} {
		w := call(h, "POST", target+q.suffix, `{"content":"one"}`, "Bearer fixture")
		if w.Code != q.status || repo.writes != 0 {
			t.Fatal(q, w)
		}
	}
	w := call(h, "POST", target, `{"content":"# hello","client_message_id":"c1"}`, "Bearer fixture")
	if w.Code != 200 || repo.writes != 1 || repo.actor != testActor || repo.room != testRoom || a.permission != "chat.messages.create" || !strings.Contains(w.Body.String(), `\\# hello`) {
		t.Fatal(w, repo, a)
	}
}
func TestInviteAcceptanceAndMemberSelfRoutes(t *testing.T) {
	a := &fakeAuthority{}
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: a}).Handler()
	w := call(h, "POST", "/api/chat/invites/"+testRoom+"/accept", `{}`, "Bearer fixture")
	if w.Code != 200 || repo.action != Accept || repo.actor != testActor {
		t.Fatal(w, repo)
	}
	w = call(h, "POST", "/api/chat/"+testRoom+"/members/me", `{"account_id":"other"}`, "Bearer fixture")
	if w.Code != 400 || repo.writes != 1 {
		t.Fatal(w, repo)
	}
}
func TestTextAndRoomValidation(t *testing.T) {
	p := Policy{77, 10, 20, 30}
	if p.Validate() != nil || ValidID(strings.ToUpper(testActor)) || ValidID("00000000-0000-0000-0000-000000000000") {
		t.Fatal("policy or ID")
	}
	bad := int32(88)
	name := "room"
	if !errors.Is(ValidateRoom(RoomPatch{Name: &name, EncryptionMode: &bad}, true, p), ErrUnsupported) {
		t.Fatal("encryption")
	}
	for _, content := range []string{"", " \n", "bad\x00text", strings.Repeat("界", 4097)} {
		if _, e := ValidateText(TextRequest{Content: content}, false); e == nil {
			t.Fatal("invalid text", len(content))
		}
	}
	nonce := "n"
	q, e := ValidateText(TextRequest{Content: "hello", Nonce: &nonce}, false)
	if e != nil || q.ClientMessageID == nil || *q.ClientMessageID != "n" || q.Nonce != nil {
		t.Fatal(q, e)
	}
	if _, e = ValidateText(TextRequest{Content: "edit", ThreadID: &nonce}, true); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}

type readRepo struct {
	calls   int
	account string
}

func (r *readRepo) Sync(_ context.Context, q messager.SyncQuery) (messager.SyncResponse, error) {
	r.calls++
	r.account = q.AccountID
	return messager.SyncResponse{Messages: []messager.MessageProjection{}}, nil
}
func (r *readRepo) ListMessages(_ context.Context, q messager.MessageListQuery) (messager.MessagePage, error) {
	r.calls++
	r.account = q.AccountID
	return messager.MessagePage{Messages: []messager.MessageProjection{}}, nil
}
func (r *readRepo) GetMessage(context.Context, messager.MessageQuery) (messager.MessageProjection, error) {
	panic("unused")
}
func TestCanonicalReadsUseExistingReader(t *testing.T) {
	a := &fakeAuthority{}
	reader := &readRepo{}
	h := (&Service{Repository: &fakeRepo{}, Authority: a, Reader: reader}).Handler()
	target := "/api/chat/" + testRoom + "/messages"
	w := call(h, "GET", target, "", "")
	if w.Code != 200 || reader.account != "" || reader.calls != 1 {
		t.Fatal(w, reader)
	}
	w = call(h, "GET", target, "", "Bearer invalid")
	if w.Code != 401 || reader.calls != 1 {
		t.Fatal(w, reader)
	}
	w = call(h, "POST", "/api/chat/"+testRoom+"/sync", `{"last_sync_timestamp":0}`, "Bearer fixture")
	if w.Code != 200 || reader.account != testActor || reader.calls != 2 {
		t.Fatal(w, reader)
	}
}

type boundAuthority struct{ fakeAuthority }

func (a *boundAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	return authority.Principal{Account: authority.Account{ID: testActor}}, nil
}
func TestBoundProofFailureNeverBecomesAnonymous(t *testing.T) {
	a := &boundAuthority{fakeAuthority: fakeAuthority{denied: authority.ErrRevoked}}
	reader := &readRepo{}
	h := (&Service{Repository: &fakeRepo{}, Authority: a, Reader: reader}).Handler()
	w := call(h, "GET", "/api/chat/"+testRoom+"/messages", "", "")
	if w.Code != 401 || reader.calls != 0 {
		t.Fatal("revoked bound proof became public read", w, reader)
	}
}
