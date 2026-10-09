// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Explicit no-body and typed JSON constructors keep null distinct from absence.
func fixtureNoBodyRequest(method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
func fixtureRawJSONRequest(method, path, token, raw string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
func fixtureJSONRequest(t *testing.T, method, path, token string, value any) *http.Request {
	t.Helper()
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	return fixtureRawJSONRequest(method, path, token, string(raw))
}
func backendJSONCall(t *testing.T, app http.Handler, method, path, token string, value any, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	app.ServeHTTP(w, fixtureJSONRequest(t, method, path, token, value))
	if w.Code != status {
		t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
	}
	return w
}
func TestFixtureJSONTransportContracts(t *testing.T) {
	nullableStringEndpoint := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(415)
			return
		}
		var v *string
		d := json.NewDecoder(r.Body)
		if e := d.Decode(&v); e != nil {
			w.WriteHeader(400)
			return
		}
		var trailing any
		if d.Decode(&trailing) != io.EOF {
			w.WriteHeader(400)
			return
		}
		if v != nil {
			w.WriteHeader(422)
			return
		}
		w.WriteHeader(204)
	})
	for _, tc := range []struct {
		r      *http.Request
		status int
	}{
		{fixtureJSONRequest(t, "POST", "/fixture", "", nil), 204},
		{fixtureNoBodyRequest("POST", "/fixture", ""), 415},
		{fixtureRawJSONRequest("POST", "/fixture", "", ""), 400},
		{fixtureJSONRequest(t, "POST", "/fixture", "", "text"), 422},
	} {
		w := httptest.NewRecorder()
		nullableStringEndpoint.ServeHTTP(w, tc.r)
		if w.Code != tc.status {
			t.Fatalf("request construction: got%d want%d", w.Code, tc.status)
		}
	}
	req := fixtureJSONRequest(t, "PATCH", "/fixture", "synthetic-token", struct {
		Title string `json:"title"`
	}{"quoted \"value\""})
	var body struct {
		Title string `json:"title"`
	}
	if e := json.NewDecoder(req.Body).Decode(&body); e != nil || body.Title != "quoted \"value\"" || req.Header.Get("Authorization") != "Bearer synthetic-token" {
		t.Fatal("typed object encoding or authorization construction")
	}
}
