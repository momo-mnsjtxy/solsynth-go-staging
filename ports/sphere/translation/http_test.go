// SPDX-License-Identifier: AGPL-3.0-only
package translation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRepo struct {
	calls   int
	request Request
}

func (f *fakeRepo) Translate(c context.Context, a string, r Request) (string, error) {
	f.calls++
	f.request = r
	return "Synthetic translated text", nil
}

type fakeAuth string

func (f fakeAuth) Authenticate(*http.Request) (string, error) { return string(f), nil }
func TestTranslationHTTPJSONAndLanguageContract(t *testing.T) {
	r := &fakeRepo{}
	h := NewHandler(r, fakeAuth("synthetic"))
	call := func(path, body string, want int) {
		t.Helper()
		q := httptest.NewRequest("POST", path, strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, q)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	call("/api/translate?to=fr&from=en", `"Synthetic source"`, 200)
	if r.calls != 1 || r.request.Source != "en" || r.request.Target != "fr" || r.request.Text != "Synthetic source" {
		t.Fatal(r)
	}
	for _, v := range []struct{ path, body string }{{"/api/translate?to=fr&to=de", `"text"`}, {"/api/translate?to=auto", `"text"`}, {"/api/translate?to=fr&from=en_US", `"text"`}, {"/api/translate?to=fr", `{"text":"no object"}`}, {"/api/translate?to=fr", `"text" {}`}, {"/api/translate?to=fr", `null`}, {"/api/translate?to=fr", "\"" + strings.Repeat("x", 2001) + "\""}} {
		call(v.path, v.body, 400)
	}
	if r.calls != 1 {
		t.Fatal("invalid request reached translation", r.calls)
	}
	w := httptest.NewRecorder()
	q := httptest.NewRequest("POST", "/api/translate?to=fr", strings.NewReader(`"text"`))
	q.Header.Set("Content-Type", "application/json")
	NewHandler(r, fakeAuth("")).ServeHTTP(w, q)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
