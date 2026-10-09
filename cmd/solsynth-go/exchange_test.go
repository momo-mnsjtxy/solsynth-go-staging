// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"solsynth.local/go-migration/internal/watchstore"
	"solsynth.local/go-migration/ports/sphere"
)

func TestExchangeFileToRouterAndProtectedBoundary(t *testing.T) {
	dir := t.TempDir()
	base, override := filepath.Join(dir, "base.json"), filepath.Join(dir, "override.json")
	if err := os.WriteFile(base, []byte(`{"Payment":{"CurrencyExchange":{"1 USD":"2.50 EUR"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte(`{"payment":{"currencyexchange":{"1 USD":"3.00 JPY"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := loadExchangeCatalog([]string{base, override})
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "watch.json")
	store, err := watchstore.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	handler := newAPIHandler(sphere.NewWatchHandler(&sphere.WatchService{Repository: store}, fixtureAuth{}), catalog)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/wallets/exchange", nil))
	want := `[{"source_currency":"USD","source_amount":1,"target_currency":"JPY","target_amount":3.00}]`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal(w.Header())
	}
	for _, method := range []string{"HEAD", "POST"} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "/api/wallets/exchange", nil))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" || w.Body.Len() != 0 {
			t.Fatalf("%s %d %s", method, w.Code, w.Body.String())
		}
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/posts/watch", nil)
	r.Header.Set("X-Test-Account", "forged")
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("protected watch became available: %d", w.Code)
	}
	if _, err = os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("anonymous catalog unexpectedly mutated persistence")
	}
}

func TestExchangeConfigSnapshotAndFailureModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "appsettings.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"Payment":{"CurrencyExchange":{"1 USD":"2 EUR"}}}`)
	snapshot, err := loadExchangeCatalog([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	write(`{"Payment":{"CurrencyExchange":{"1 USD":"3 EUR"}}}`)
	options, err := snapshot.Options()
	if err != nil || len(options) != 1 || options[0].TargetAmount.String() != "2" {
		t.Fatalf("snapshot mutated: %+v %v", options, err)
	}
	refreshed, err := loadExchangeCatalog([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	options, err = refreshed.Options()
	if err != nil || options[0].TargetAmount.String() != "3" {
		t.Fatal(options, err)
	}
	write(`{"Payment":{"CurrencyExchange":{"1 USD":"1 usd"}}}`)
	invalid, err := loadExchangeCatalog([]string{path})
	if err != nil {
		t.Fatal("invalid target is a request-time catalog error, not JSON load error", err)
	}
	handler := newAPIHandler(sphere.NewWatchHandler(nil, nil), invalid)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/wallets/exchange", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"code":"WALLET_EXCHANGE_CONFIGURATION_INVALID"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	write(`{`)
	if _, err = loadExchangeCatalog([]string{path}); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	if _, err = loadExchangeCatalog([]string{filepath.Join(dir, "missing.json")}); err == nil {
		t.Fatal("missing configuration accepted")
	}
	empty, err := loadExchangeCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler = newAPIHandler(sphere.NewWatchHandler(nil, nil), empty)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/wallets/exchange", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/posts/watch", nil))
	if w.Code != 503 {
		t.Fatal("disabled watch was not fail-closed", w.Code)
	}
	var paths configFiles
	if err = paths.Set(""); err == nil {
		t.Fatal("empty flag accepted")
	}
	if err = paths.Set("a.json"); err != nil {
		t.Fatal(err)
	}
	if err = paths.Set("b.json"); err != nil || len(paths) != 2 || paths[0] != "a.json" || paths[1] != "b.json" {
		t.Fatal(paths, err)
	}
}
