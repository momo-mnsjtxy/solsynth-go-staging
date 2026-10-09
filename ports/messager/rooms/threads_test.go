// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/messager"
	"testing"
)

type threadFake struct {
	fakeRepo
	calls int
	ids   []string
	key   string
}

func (r *threadFake) Thread(_ context.Context, actor, rid, mid string, take, offset int) (ThreadPage, error) {
	r.calls++
	return ThreadPage{Root: messager.MessageProjection{ID: mid}, Replies: []ThreadReply{}, Total: 3}, nil
}
func (r *threadFake) Redirect(_ context.Context, actor, rid string, ids []string, key string) (messager.MessageProjection, error) {
	r.calls++
	r.ids = ids
	r.key = key
	return messager.MessageProjection{ID: testRoom}, nil
}
func TestThreadAndRedirectRoutesUseCurrentIdentityAndStrictShapes(t *testing.T) {
	a := &fakeAuthority{}
	repo := &threadFake{}
	h := (&Service{Repository: repo, Authority: a}).Handler()
	w := call(h, "GET", "/api/chat/"+testRoom+"/messages/"+testActor+"/thread?take=1&offset=2", "", "Bearer fixture")
	if w.Code != 200 || w.Header().Get("X-Total") != "3" || repo.calls != 1 {
		t.Fatal(w, repo)
	}
	for _, tail := range []string{"?take=0", "?take=101", "?take=1&take=2", "?identity=x", "?offset=100001", "?token=x", "?unknown=1"} {
		w = call(h, "GET", "/api/chat/"+testRoom+"/messages/"+testActor+"/thread"+tail, "", "Bearer fixture")
		if w.Code < 400 || repo.calls != 1 {
			t.Fatal(tail, w)
		}
	}
	w = call(h, "POST", "/api/chat/"+testRoom+"/messages/redirect", `{"message_ids":["`+testActor+`"]}`, "Bearer fixture")
	if w.Code != 200 || repo.calls != 2 || a.permission != "chat.messages.create" || len(repo.ids) != 1 {
		t.Fatal(w, repo, a)
	}
	w = call(h, "POST", "/api/chat/"+testRoom+"/messages/redirect", `{"message_ids":[],"source_room_id":"forged"}`, "Bearer fixture")
	if w.Code != 400 || repo.calls != 2 {
		t.Fatal(w)
	}
}
func TestTextThreadsReferencesAndReservedSnapshotValidation(t *testing.T) {
	id := testRoom
	for _, p := range []TextRequest{{Content: "root", IsThreadRoot: true}, {Content: "reply", ThreadID: &id}, {ForwardedMessageID: &id}} {
		if _, e := ValidateText(p, false); e != nil {
			t.Fatal(p, e)
		}
	}
	for _, p := range []TextRequest{{Content: "x", IsThreadRoot: true, ThreadID: &id}, {Content: "x", IsThreadRoot: true, RepliedMessageID: &id}, {Content: "x", Meta: json.RawMessage(`{"redirect":null}`)}} {
		if _, e := ValidateText(p, false); !errors.Is(e, ErrInvalid) {
			t.Fatal(p, e)
		}
	}
}
