// SPDX-License-Identifier: AGPL-3.0-only
package nodeinfo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This port-level reader double tests response mapping only. Repository success
// is exclusively tested with the real disposable PostgreSQL integration suite.
type readerFunc func(context.Context) (Stats, error)

func (f readerFunc) ReadStats(ctx context.Context) (Stats, error) { return f(ctx) }

func request(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestDefaultHandlerDeniesWithoutReader(t *testing.T) {
	reader := readerFunc(func(context.Context) (Stats, error) { t.Fatal("disabled handler read storage"); return Stats{}, nil })
	for _, path := range []string{"/.well-known/nodeinfo", "/.well-known/nodeinfo/2.0", "/.well-known/nodeinfo/2.1", "/unknown"} {
		w := request(NewHandler(reader, nil), http.MethodGet, path)
		if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"nodeinfo_policy_unverified"}` {
			t.Fatal(path, w)
		}
	}
}

func TestDiscoveryDoesNotReadAndIgnoresRequestHost(t *testing.T) {
	domain := "fixture.example"
	h := NewSyntheticCompatibilityHandler(readerFunc(func(context.Context) (Stats, error) { t.Fatal("discovery read storage"); return Stats{}, nil }), &domain)
	domain = "mutated.example" // Handler owns an immutable copy of configuration.
	r := httptest.NewRequest(http.MethodGet, "http://spoof.example/.well-known/nodeinfo?ignored=yes", nil)
	r.Header.Set("X-Forwarded-Host", "spoof.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatal(w)
	}
	compareJSON(t, w.Body.Bytes(), `{"links":[{"rel":"http://nodeinfo.diaspora.software/ns/schema/2.1","href":"https://fixture.example/nodeinfo/2.1"},{"rel":"http://nodeinfo.diaspora.software/ns/schema/2.0","href":"https://fixture.example/nodeinfo/2.0"}]}`)
	if w := request(NewSyntheticCompatibilityHandler(nil, nil), http.MethodGet, "/.well-known/nodeinfo"); w.Code != 200 {
		t.Fatal(w)
	}
}

func TestHTTPVersionDocuments(t *testing.T) {
	calls := 0
	h := NewSyntheticCompatibilityHandler(readerFunc(func(context.Context) (Stats, error) {
		calls++
		return Stats{TotalPublishers: 7, ActivePublishers: 3, TotalPosts: 11}, nil
	}), nil)
	for _, tc := range []struct{ version, want string }{
		{"2.0", `{` + commonExpected + `"configuration":{"urls":{},` + configExpected + `}}`},
		{"2.1", `{"version":"2.1",` + commonExpected + `"configuration":{` + configExpected + `},"metadata":{}}`},
	} {
		w := request(h, http.MethodGet, "/.well-known/nodeinfo/"+tc.version+"?unused=value")
		if w.Code != 200 {
			t.Fatal(w)
		}
		compareJSON(t, w.Body.Bytes(), tc.want)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestCandidateRoutingMethodAndIdentityGuards(t *testing.T) {
	h := NewSyntheticCompatibilityHandler(readerFunc(func(context.Context) (Stats, error) { t.Fatal("invalid request read storage"); return Stats{}, nil }), nil)
	for _, path := range []string{"/nodeinfo/2.0", "/nodeinfo/2.1", "/.well-known/nodeinfo/", "/.well-known/nodeinfo/2.1/", "/.well-known/nodeinfo/3.0", "/.WELL-KNOWN/nodeinfo", "/"} {
		if w := request(h, http.MethodGet, path); w.Code != 404 {
			t.Fatal(path, w)
		}
	}
	for _, path := range []string{"/.well-known/nodeinfo", "/.well-known/nodeinfo/2.0", "/.well-known/nodeinfo/2.1"} {
		for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodOptions, http.MethodPut, http.MethodDelete} {
			w := request(h, method, path)
			if w.Code != 405 || w.Header().Get("Allow") != "GET" || method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatal(method, path, w)
			}
		}
		for _, key := range []string{"Authorization", "Cookie", "X-Test-Account", "authorization"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header[key] = []string{""} // Header presence is sufficient, including an empty value.
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(key, w)
			}
		}
	}
}

func TestReaderFailuresCancellationAndInvalidStatsAreSanitized(t *testing.T) {
	for _, reader := range []Reader{
		nil,
		readerFunc(func(context.Context) (Stats, error) {
			return Stats{TotalPublishers: 99}, errors.New("private SQL DSN details")
		}),
		readerFunc(func(context.Context) (Stats, error) { return Stats{TotalPublishers: -1}, nil }),
		readerFunc(func(context.Context) (Stats, error) { return Stats{ActivePublishers: -1}, nil }),
		readerFunc(func(context.Context) (Stats, error) { return Stats{TotalPosts: -1}, nil }),
	} {
		for _, path := range []string{"/.well-known/nodeinfo/2.0", "/.well-known/nodeinfo/2.1"} {
			w := request(NewSyntheticCompatibilityHandler(reader, nil), http.MethodGet, path)
			if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"nodeinfo_unavailable"}` {
				t.Fatal(w)
			}
		}
	}
	for _, path := range []string{"/.well-known/nodeinfo", "/.well-known/nodeinfo/2.0", "/.well-known/nodeinfo/2.1"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		w := httptest.NewRecorder()
		NewSyntheticCompatibilityHandler(readerFunc(func(context.Context) (Stats, error) { t.Fatal("cancelled request read"); return Stats{}, nil }), nil).ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatal(w)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/.well-known/nodeinfo/2.1", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	NewSyntheticCompatibilityHandler(readerFunc(func(context.Context) (Stats, error) { cancel(); return Stats{}, nil }), nil).ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal(w)
	}
}
