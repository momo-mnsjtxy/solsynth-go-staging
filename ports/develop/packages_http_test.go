// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPackageHTTPGuards(t *testing.T) {
	repo := &packageFixtureRepository{app: PackageRecord{ID: "id", Slug: "example", StorageKey: packageString("file.zip")}}
	svc := NewSyntheticCompatibilityPackageService(repo, &packageFixtureStorage{data: []byte("ZIP fixture")})
	for _, h := range []http.Handler{NewPackageHandler(svc), NewSyntheticCompatibilityPackageHandler(NewPackageService(repo, nil)), NewSyntheticCompatibilityPackageHandler(nil)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/miniapps/example/package", nil))
		if w.Code != 503 || len(repo.calls) != 0 {
			t.Fatal(w.Code, repo.calls)
		}
	}
	h := NewSyntheticCompatibilityPackageHandler(svc)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/miniapps/example", 404}, {http.MethodGet, "/api/miniapps//package", 404}, {http.MethodGet, "/api/miniapps/example/package/extra", 404},
		{http.MethodHead, "/api/miniapps/example/package", 405}, {http.MethodPost, "/api/miniapps/example/package", 405},
		{http.MethodGet, "/api/miniapps/a%5Cb/package", 400},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || len(repo.calls) != 0 {
			t.Fatal(tc, w.Code, repo.calls)
		}
	}
	for _, key := range []string{"Authorization", "Cookie", "X-Test-Account", "authorization"} {
		r := httptest.NewRequest(http.MethodGet, "/api/miniapps/example/package", nil)
		r.Header[key] = nil
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || len(repo.calls) != 0 {
			t.Fatal(key, w.Code, repo.calls)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/miniapps/example/package?ignored=1", nil))
	if w.Code != 200 || w.Body.String() != "ZIP fixture" || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Content-Disposition") != "attachment; filename=example.zip" || w.Header().Get("Content-Length") != "11" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}

func TestPackageHTTPErrorEnvelopesAndRedirect(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrPackageNotFound, 404, "DEV_MINI_APP_NOT_FOUND"}, {ErrPackageMissing, 404, "DEV_MINI_APP_NO_PACKAGE"}, {errors.New("secret DSN detail"), 503, "packages_unavailable"}, {context.Canceled, 503, "packages_unavailable"},
	} {
		repo := &packageFixtureRepository{lookupErr: tc.err}
		h := NewSyntheticCompatibilityPackageHandler(NewSyntheticCompatibilityPackageService(repo, nil))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/miniapps/example/package", nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		validBody := strings.TrimSpace(w.Body.String()) == `{"error":"`+tc.code+`"}`
		if tc.status == 404 {
			wantMessage := "Mini app not found"
			if tc.err == ErrPackageMissing {
				wantMessage = "Mini app has no published package"
			}
			validBody = len(body) == 3 && body["code"] == tc.code && body["status"] == float64(404) && body["message"] == wantMessage
		}
		if w.Code != tc.status || !validBody || w.Header().Get("Location") != "" || w.Header().Get("Content-Disposition") != "" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
	}
	repo := &packageFixtureRepository{app: PackageRecord{ID: "id", Slug: "example", StorageKey: packageString("file.zip"), URL: packageString("https://example.invalid/file.zip")}}
	h := NewSyntheticCompatibilityPackageHandler(NewSyntheticCompatibilityPackageService(repo, nil))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/miniapps/example/package", nil))
	if w.Code != 302 || w.Header().Get("Location") != "https://example.invalid/file.zip" || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}
