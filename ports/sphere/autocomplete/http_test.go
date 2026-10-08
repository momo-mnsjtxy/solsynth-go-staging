// SPDX-License-Identifier: AGPL-3.0-only
package autocomplete

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRepo struct {
	calls                int
	actor, room, content string
}

func (f *fakeRepo) Suggest(c context.Context, actor, room, content string) ([]Suggestion, error) {
	f.calls++
	f.actor, f.room, f.content = actor, room, content
	return []Suggestion{}, nil
}

type fakeAuth string

func (f fakeAuth) Authenticate(*http.Request) (string, error) { return string(f), nil }
func TestAutocompletePrefixesAndHTTP(t *testing.T) {
	for _, tc := range []struct{ content, kind, prefix, text string }{{"@abc", "user", "@", "abc"}, {"@u/abc", "user", "@u/", "abc"}, {"@p/abc", "publisher", "@p/", "abc"}, {"@r/abc", "realm", "@r/", "abc"}, {":pack+item", "sticker", ":", "pack+item"}, {"#tag", "", "", ""}, {"@unknown/x", "", "", ""}} {
		q, e := Parse(tc.content)
		if e != nil || q.Kind != tc.kind || q.Prefix != tc.prefix || q.Text != tc.text {
			t.Fatal(tc, q, e)
		}
	}
	r := &fakeRepo{}
	h := NewHandler(r, fakeAuth("synthetic"))
	id := "d5100000-0000-4000-8000-000000005400"
	for _, path := range []string{"/api/autocomplete", "/api/chat/" + id + "/autocomplete", "/api/autocomplete?roomId=" + id} {
		q := httptest.NewRequest("POST", path, strings.NewReader(`{"content":"@abc"}`))
		q.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if r.calls != 3 || r.room != id || r.content != "@abc" {
		t.Fatal(r)
	}
	for _, body := range []string{`{"content":"x","content":"y"}`, `{"content":"x","limit":100}`, `{}`, `{"content":null}`, `{"content":"x"} {}`} {
		q := httptest.NewRequest("POST", "/api/autocomplete", strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	if r.calls != 3 {
		t.Fatal("invalid body reached repository", r.calls)
	}
}
