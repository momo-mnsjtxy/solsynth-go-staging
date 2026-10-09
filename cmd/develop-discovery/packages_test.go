// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPackageConfigurationRequiresIndependentOptIn(t *testing.T) {
	for _, args := range [][]string{
		{"--synthetic-miniapp-stage=2"},
		{"--synthetic-package-directory=/tmp/synthetic"},
		{"--synthetic-production-status=42", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-package-directory=/tmp/synthetic"},
		{"--synthetic-production-status=42", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-miniapp-stage=2"},
		{"--synthetic-production-status=42", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-miniapp-stage=2147483648", "--synthetic-package-directory=/tmp/synthetic"},
	} {
		if _, err := parseConfig(args); err == nil {
			t.Fatalf("accepted incomplete package configuration %q", args)
		}
	}
	cfg, err := parseConfig([]string{"--synthetic-production-status=42", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-miniapp-stage=2", "--synthetic-package-directory=/tmp/synthetic"})
	if err != nil || cfg.syntheticMiniAppStage == nil || *cfg.syntheticMiniAppStage != 2 {
		t.Fatalf("valid package configuration: %#v %v", cfg, err)
	}
}

func TestPackageDefaultRequiresNoDatabaseOrFiles(t *testing.T) {
	cfg, _ := parseConfig(nil)
	h, closeDB, err := makeHandler(context.Background(), cfg, func(string) string { t.Fatal("read DSN"); return "" }, func(string, string) (*sql.DB, error) { t.Fatal("opened database"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/miniapps/synthetic/package", nil))
	if w.Code != 503 {
		t.Fatalf("default package status %d", w.Code)
	}
}

func TestDevelopRouteFamiliesRemainSeparate(t *testing.T) {
	calls := []string{}
	record := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls = append(calls, name); w.WriteHeader(204) })
	}
	h := newDevelopHandler(record("apps"), record("packages"))
	for _, path := range []string{"/api/apps", "/api/miniapps/synthetic/package", "/api/apps/synthetic/products"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	if len(calls) != 3 || calls[0] != "apps" || calls[1] != "packages" || calls[2] != "apps" {
		t.Fatal(calls)
	}
}
