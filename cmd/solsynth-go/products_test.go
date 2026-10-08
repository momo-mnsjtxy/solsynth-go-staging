// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalletProductConfigurationAndHostBoundary(t *testing.T) {
	disabled, err := makeWalletProductHandler(walletProductFlags{})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	disabled.ServeHTTP(w, httptest.NewRequest("GET", "/api/wallet-products/catalog", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if _, err = makeWalletProductHandler(walletProductFlags{enabled: true}); err == nil {
		t.Fatal("guessed missing currency")
	}
	if _, err = makeWalletProductHandler(walletProductFlags{defaultCurrencySet: true, defaultCurrency: "golds"}); err == nil {
		t.Fatal("enabled through default alone")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err = os.WriteFile(path, []byte(`{"Payment":{"Product":{"GoldCurrency":{"Currency":"DOC","ProviderMappings":{"Paddle":{"PriceID":"2.50"}}}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	products, err := makeWalletProductHandler(walletProductFlags{enabled: true, configs: configFiles{path}})
	if err != nil {
		t.Fatal(err)
	}
	watch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	host := newAPIHandler(watch, nil, nil, products)
	w = httptest.NewRecorder()
	host.ServeHTTP(w, httptest.NewRequest("GET", "/api/wallet-products/catalog", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"paddle":{"price_id":2.50}`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	host.ServeHTTP(w, httptest.NewRequest("GET", "/api/posts/watch", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	host.ServeHTTP(w, httptest.NewRequest("POST", "/api/wallet-products/golds-resupply-pack/checkout/paddle", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
