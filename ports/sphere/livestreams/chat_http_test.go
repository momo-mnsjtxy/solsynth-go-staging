// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type chatHTTPRepo struct {
	fakeRepo
	action, content, key, message string
	limit, offset, minutes        int
}

func (r *chatHTTPRepo) ListChat(_ context.Context, who, id string, l, o int) ([]ChatMessage, error) {
	r.called++
	r.who = who
	r.action = "list"
	r.limit = l
	r.offset = o
	return []ChatMessage{}, nil
}
func (r *chatHTTPRepo) SendChat(_ context.Context, who, id, content, key string) (ChatMessage, error) {
	r.called++
	r.who = who
	r.action = "send"
	r.content = content
	r.key = key
	return ChatMessage{ID: stream, SenderID: who, Content: content, DeliveryState: "pending"}, nil
}
func (r *chatHTTPRepo) DeleteChat(_ context.Context, who, id, mid string) error {
	r.called++
	r.who = who
	r.action = "delete"
	r.message = mid
	return nil
}
func (r *chatHTTPRepo) TimeoutChat(_ context.Context, who, id, mid, key string, q ChatTimeout) error {
	r.called++
	r.who = who
	r.action = "timeout"
	r.message = mid
	r.key = key
	r.minutes = 10
	if q.DurationMinutes != nil {
		r.minutes = *q.DurationMinutes
	}
	return nil
}
func TestLiveChatHTTPContractAndStrictInputs(t *testing.T) {
	repo := &chatHTTPRepo{}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
	base := "/api/livestreams/" + stream + "/chat"
	if w := request(h, "GET", base, "", ""); w.Code != 401 || repo.called != 0 {
		t.Fatal(w)
	}
	if w := request(h, "GET", base+"?limit=5&offset=2", "", "Bearer synthetic"); w.Code != 200 || repo.limit != 5 || repo.offset != 2 || repo.who != actor {
		t.Fatal(w, repo)
	}
	for _, body := range []string{`{"sender_id":"forged","content":"x"}`, `{"content":"x","content":"y"}`, `{"Content":"x"}`, `null`} {
		before := repo.called
		if w := request(h, "POST", base, body, "Bearer synthetic"); w.Code != 400 || repo.called != before {
			t.Fatal(w)
		}
	}
	r := httptest.NewRequest("POST", base, strings.NewReader(`{"content":"synthetic message"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("Idempotency-Key", "synthetic-request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || repo.content != "synthetic message" || repo.key != "synthetic-request" || !strings.Contains(w.Body.String(), `"delivery_state":"pending"`) {
		t.Fatal(w, repo)
	}
	if w = request(h, "POST", base+"/"+stream+"/timeout", `{"duration_minutes":0}`, "Bearer synthetic"); w.Code != 200 || repo.minutes != 0 || repo.action != "timeout" {
		t.Fatal(w, repo)
	}
	if w = request(h, "DELETE", base+"/"+stream, "", "Bearer synthetic"); w.Code != 200 || repo.action != "delete" {
		t.Fatal(w, repo)
	}
	before := repo.called
	r = httptest.NewRequest("POST", base, strings.NewReader(`{"content":"x"}`))
	r.Header.Set("Authorization", "Bearer synthetic")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 || repo.called != before {
		t.Fatal(w)
	}
	for _, path := range []string{base + "?limit=1&limit=2", base + "?offset=-1", base + "?actor=forged"} {
		if w = request(h, "GET", path, "", "Bearer synthetic"); w.Code != 400 {
			t.Fatal(w)
		}
	}
}
