// SPDX-License-Identifier: AGPL-3.0-only
package domaintrust

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fake struct {
	Repository
	calls  int
	result Result
	err    error
}

func (f *fake) Validate(context.Context, string) (Result, error) { f.calls++; return f.result, f.err }
func TestPublicValidationProjectionAndRequestURI(t *testing.T) {
	f := &fake{result: Result{IsAllowed: true, MatchedRule: &Rule{ID: "private-rule", Reason: ptr("private operator metadata")}}}
	h := NewHandler(f, nil)
	for _, method := range []string{"GET", "POST"} {
		path := "/api/domain-blocks/check?url=https%3A%2F%2Fexample.test%2Fprivate%3Ftoken%3DSYNTHETIC"
		body := ""
		if method == "POST" {
			path = "/api/domain-blocks/validate"
			body = `{"url":"https://example.test/private?token=SYNTHETIC"}`
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		original := r.RequestURI
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || r.RequestURI != original || strings.Contains(w.Body.String(), "private-rule") || strings.Contains(w.Body.String(), "SYNTHETIC") {
			t.Fatal(w.Code, w.Body.String(), r.RequestURI)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Header())
		}
	}
}
func TestDomainHTTPErrorsNeverEchoURL(t *testing.T) {
	raw := "https://example.test/private?secret=SYNTHETIC"
	f := &fake{err: errors.New(raw)}
	w := httptest.NewRecorder()
	NewHandler(f, nil).ServeHTTP(w, httptest.NewRequest("GET", "/api/domain-blocks/check?url="+raw, nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), "SYNTHETIC") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{"url":"https://example.test","url":"https://other.test"}`, `{"url":"https://example.test","actor_id":"forged"}`, `{"url":null}`, `{"url":5}`} {
		r := httptest.NewRequest("POST", "/api/domain-blocks/validate", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		NewHandler(f, nil).ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
}
func TestDomainAdminRouteRequiresCanonicalAuth(t *testing.T) {
	f := &fake{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/domain-blocks", nil)
	r.Header.Set("X-Account-Id", "forged")
	NewHandler(f, func(*http.Request, bool) (string, error) { return "", nil }).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
}
