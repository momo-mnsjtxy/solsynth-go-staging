// SPDX-License-Identifier: AGPL-3.0-only
package taxonomy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPDefaultDenyEveryRoute(t *testing.T) {
	for _, path := range []string{"/api/posts/tags", "/api/posts/tags/quota", "/api/posts/tags/x/quota", "/api/posts/categories", "/api/admin/tags", "/api/admin/posts/tags/x/assign", "/api/admin/categories/x", "/api/posts/tags/x/subscribe", "/api/categories/subscriptions"} {
		for _, h := range []http.Handler{NewHandler(service(t, repo(), authority())), NewSyntheticHandler(NewService(repo())), NewSyntheticHandler(nil)} {
			w := request(h, "GET", path, "")
			if w.Code != 503 {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
	}
}
func TestHTTPProjectionAndAliases(t *testing.T) {
	m := repo()
	s := service(t, m, authority())
	h := NewSyntheticHandler(s)
	for _, base := range []string{"/api/admin/tags", "/api/admin/posts/tags"} {
		w := request(h, "POST", base, `{"slug":"  Hello  ","name":"Greeting"}`)
		if base == "/api/admin/posts/tags" {
			if w.Code != 400 {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 201 || w.Header().Get("Location") != base+"/hello" {
			t.Fatal(w.Code, w.Body.String())
		}
		var raw map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw) != 9 || raw["slug"] != "hello" {
			t.Fatal(raw, err)
		}
		if _, ok := raw["created_at"]; ok {
			t.Fatal("unverified inherited entity field exposed")
		}
	}
	for _, path := range []string{"/api/posts/tags", "/api/admin/tags", "/api/admin/posts/tags"} {
		w := request(h, "GET", path, "")
		if w.Code != 200 || w.Header().Get("X-Total") != "1" || !strings.Contains(w.Header().Get("X-Solsynth-Compatibility"), "unverified") {
			t.Fatal(w.Code, w.Header())
		}
	}
	w := request(h, "GET", "/api/admin/posts/tags/HELLO", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "PATCH", "/api/admin/posts/tags/hello", `{"name":"","description":null}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "DELETE", "/api/admin/posts/tags/hello", "")
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestHTTPValidationAndCredentialRefusal(t *testing.T) {
	h := NewSyntheticHandler(service(t, repo(), authority()))
	for _, body := range []string{`null`, `[]`, `{`, `{} {}`, `{"slug":"x","unexpected":1}`, `{"slug":3}`, `{"slug":null}`, `{"slug":"` + strings.Repeat("x", 70000) + `"}`} {
		w := request(h, "POST", "/api/posts/tags", body)
		if w.Code != 400 {
			t.Fatal(w.Code, body[:min(len(body), 100)])
		}
	}
	for _, path := range []string{"/api/posts/tags?take=bad", "/api/posts/tags?offset=-1", "/api/posts/tags?take=2147483648", "/api/admin/tags?isProtected=1", "/api/admin/tags?ownerPublisherId=bad"} {
		if w := request(h, "GET", path, ""); w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account", "X-Account-ID", "X-User", "X-Auth-Token", "Proxy-Authorization"} {
		r := httptest.NewRequest("GET", "/api/posts/tags", nil)
		r.Header[header] = []string{""}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(header, w.Code)
		}
	}
	for _, path := range []string{"/api/posts/tags?access_token=x", "/api/posts/tags?actor_id=x"} {
		if w := request(h, "GET", path, ""); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/posts/tags", strings.NewReader(`{"slug":"x"}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
}
func TestHTTPQueriesMethodsAndFailureSanitization(t *testing.T) {
	m := repo()
	h := NewSyntheticHandler(service(t, m, authority()))
	w := request(h, "GET", "/api/admin/tags?offset=-5&take=500&query=%20hi%20&isProtected=false&isEvent=true&unowned=true&ownerPublisherId="+publisher, "")
	if w.Code != 200 || m.query.Offset != 0 || m.query.Take != 200 || m.query.Search != "hi" || m.query.IsProtected == nil || *m.query.IsProtected || !m.query.Now.Equal(fixedNow) {
		t.Fatal(w.Code, m.query)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"PUT", "/api/posts/tags", 405}, {"POST", "/api/posts/categories", 405}, {"PATCH", "/api/posts/categories/x", 405}, {"DELETE", "/api/posts/tags/x", 405}, {"GET", "/api/posts/tags/x/event", 404}, {"GET", "/api/admin/tags/x/event", 405}, {"GET", "/api/posts/tags/x/missing", 404}, {"GET", "/unrelated", 404}, {"GET", "/api/posts/tags/", 404}} {
		if w := request(h, tc.method, tc.path, ""); w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
	}
	m.err = errors.New("private connection details")
	for _, path := range []string{"/api/posts/tags", "/api/posts/tags/missing"} {
		w := request(h, "GET", path, "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestHTTPQuotaAndSubscriptions(t *testing.T) {
	h := NewSyntheticHandler(service(t, repo(), authority()))
	for _, path := range []string{"/api/posts/tags/quota", "/api/posts/tags/ignored/quota", "/api/categories/subscriptions", "/api/posts/categories/a/subscription", "/api/posts/tags/a/subscription"} {
		if w := request(h, "GET", path, ""); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, k := range []string{"tags", "categories"} {
		if w := request(h, "POST", "/api/posts/"+k+"/a/unsubscribe", ""); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
}

func TestHTTPStoredProjectionEncodingFailureIsNotSuccess(t *testing.T) {
	m := repo()
	invalidTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	m.rows["tags:invalid"] = Record{Kind: Tag, ID: actor, Slug: "invalid", EventEndsAt: &invalidTime}
	h := NewSyntheticHandler(service(t, m, authority()))
	for _, path := range []string{"/api/posts/tags", "/api/posts/tags/invalid"} {
		w := request(h, "GET", path, "")
		if w.Code != 503 || w.Header().Get("X-Total") != "" || w.Header().Get("Cache-Control") != "no-store" || w.Body.String() != "{\"error\":\"taxonomy_unavailable\"}\n" {
			t.Fatal(path, w.Code, w.Header(), w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	w.Header().Set("Location", "/must-not-report-success")
	writeJSON(w, 201, invalidTime)
	if w.Code != 503 || w.Header().Get("Location") != "" {
		t.Fatal(w.Code, w.Header())
	}
}
