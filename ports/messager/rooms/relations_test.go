// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"strings"
	"testing"
	"time"
)

type relationFake struct {
	fakeRepo
	direct  DirectRequest
	changed string
}

func (r *relationFake) CreateDirect(_ context.Context, actor string, p DirectRequest) (Room, error) {
	r.writes++
	r.actor, r.direct = actor, p
	return Room{ID: testRoom, Type: 1}, nil
}
func (r *relationFake) GetDirect(context.Context, string, string) (Room, error) {
	return Room{ID: testRoom}, nil
}
func (r *relationFake) ChangeDirect(_ context.Context, actor, rid, action string) (Room, error) {
	r.writes++
	r.actor, r.changed = actor, action
	return Room{ID: rid, Members: []Member{{AccountID: actor}}}, nil
}
func (r *relationFake) RealmRooms(context.Context, string, string, int, int) ([]Room, error) {
	return []Room{}, nil
}
func (r *relationFake) Timeout(context.Context, string, string, string, *TimeoutRequest) error {
	return nil
}

func TestDirectTransportAndRoleScope(t *testing.T) {
	r := &relationFake{}
	a := &fakeAuthority{}
	h := (&Service{Repository: r, Authority: a}).Handler()
	if w := call(h, "POST", "/api/chat/direct", `{"related_user_id":"`+testActor+`"}`, "Bearer fixture"); w.Code != 200 || r.direct.RelatedUserID != testActor || r.actor != testActor || a.permission != "chat.create" {
		t.Fatal(w, r, a)
	}
	if w := call(h, "POST", "/api/chat/"+testRoom+"/confirm", `{}`, "Bearer fixture"); w.Code != 200 || r.changed != "confirm" {
		t.Fatal(w, r)
	}
	if w := call(h, "DELETE", "/api/chat/"+testRoom+"/block", `{}`, "Bearer fixture"); w.Code != 200 || r.changed != "unblock" {
		t.Fatal(w, r)
	}
	if w := call(h, "POST", "/api/chat/invites/"+testRoom, `{"related_user_id":"`+testActor+`","role":50}`, "Bearer fixture"); w.Code != 501 {
		t.Fatal(w)
	}
	if w := call(h, "GET", "/api/realms/synthetic/chat?take=101", ``, "Bearer fixture"); w.Code != 400 {
		t.Fatal(w)
	}
}

func TestTimeoutValidation(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, text := range []string{"bad\x00reason", string([]byte{0xff}), strings.Repeat("x", 4097)} {
		if ValidateTimeout(&TimeoutRequest{TimeoutUntil: at, Reason: &text}) == nil {
			t.Fatal("invalid timeout reason accepted")
		}
	}
	text := strings.Repeat("界", 4096)
	if ValidateTimeout(&TimeoutRequest{TimeoutUntil: at, Reason: &text}) != nil || ValidateTimeout(nil) != nil || ValidateTimeout(&TimeoutRequest{}) == nil {
		t.Fatal("timeout validation boundary")
	}
}
