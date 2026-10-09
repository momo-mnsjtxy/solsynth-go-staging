// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRichMessageStrictFieldsAndBounds(t *testing.T) {
	good := []string{`{"content":"plain","attachments_id":null,"meta":null,"embeds":null}`, `{"attachments_id":["c2000000-0000-4000-8000-000000000001"]}`, `{"content":null,"attachments_id":["c2000000-0000-4000-8000-000000000001"]}`, `{"content":"caption","meta":{"custom":"value"},"embeds":[{"type":"link","url":"https://example.invalid"}]}`}
	for _, body := range good {
		var p TextRequest
		if e := json.Unmarshal([]byte(body), &p); e != nil {
			t.Fatal(e)
		}
		if _, e := ValidateText(p, false); e != nil {
			t.Fatal(body, e)
		}
	}
	for _, body := range []string{`{"attachments_id":["https://example.invalid"]}`, `{"attachments_id":["c2000000-0000-4000-8000-000000000001","c2000000-0000-4000-8000-000000000001"]}`, `{"meta":[]}`, `{"embeds":[null]}`} {
		var p TextRequest
		e := json.Unmarshal([]byte(body), &p)
		if e == nil {
			_, e = ValidateText(p, false)
		}
		if e == nil {
			t.Fatal("accepted", body)
		}
	}
	var p TextRequest
	if e := json.Unmarshal([]byte(`{"content":"","attachments_id":[]}`), &p); e != nil || !p.ContentSet {
		t.Fatal(p, e)
	}
	if _, e := ValidateText(p, true); e != nil {
		t.Fatal("edit final content validated transactionally", e)
	}
}

type reactionFake struct {
	*fakeRepo
	calls   int
	request ReactionRequest
}

func (f *reactionFake) React(_ context.Context, a, r, m string, p ReactionRequest) (*Reaction, error) {
	f.calls++
	f.request = p
	if f.calls%2 == 0 {
		return nil, nil
	}
	return &Reaction{ID: testRoom, MessageID: m, SenderID: a, Symbol: p.Symbol, Attitude: p.Attitude}, nil
}
func (f *reactionFake) RemoveReaction(context.Context, string, string, string, string) error {
	f.calls++
	return nil
}
func (f *reactionFake) ListReactions(context.Context, string, string, string, ReactionQuery) ([]Reaction, int, error) {
	return []Reaction{}, 0, nil
}
func TestReactionHTTPContractsAndAuthority(t *testing.T) {
	f := &reactionFake{fakeRepo: &fakeRepo{}}
	a := &fakeAuthority{}
	h := (&Service{Repository: f, Authority: a}).Handler()
	target := "/api/chat/" + testRoom + "/messages/" + testActor + "/reactions"
	for _, want := range []int{200, 204} {
		w := call(h, "POST", target, `{"symbol":"heart","attitude":73}`, "Bearer fixture")
		if w.Code != want || f.request.Attitude != 73 || a.permission != "chat.messages.react" {
			t.Fatal(w.Code, w.Body.String(), a)
		}
	}
	w := call(h, "GET", target+"?take=20&order=created", "", "")
	if w.Code != 200 || w.Header().Get("X-Total") != "0" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{"symbol":"heart","symbol":"clap"}`, `{"symbol":"heart","attitude":1.5}`, `{"symbol":"heart","sender_id":"x"}`} {
		before := f.calls
		w := call(h, "POST", target, body, "Bearer fixture")
		if w.Code != 400 || f.calls != before {
			t.Fatal(w.Code, body)
		}
	}
	a.denied = ErrForbidden
	before := f.calls
	w = call(h, "POST", target, `{"symbol":"heart"}`, "Bearer fixture")
	if w.Code != 403 || f.calls != before {
		t.Fatal(w.Code)
	}
}
