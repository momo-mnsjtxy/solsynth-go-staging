// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http/httptest"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
	"strings"
	"testing"
)

func TestOwnedPublicCatalogComposition(t *testing.T) {
	provider, err := exchangecatalog.LoadJSON(strings.NewReader(`{"Payment":{"Product":{"GoldCurrency":{"Currency":"test-golds"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	public, err := publicCatalogs(Config{CatalogProviders: []exchangecatalog.Provider{provider}})
	if err != nil {
		t.Fatal(err)
	}
	h := router{modules: modules{public: bindIdentity(nil, public)}}
	for _, path := range []string{"/.well-known/badges", "/wallet/wallets/exchange", "/api/wallets/exchange", "/wallet/wallet-products/catalog"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
		}
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer invalid")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatalf("supplied identity was ignored: %s %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/wallet/wallets/exchange", strings.NewReader(`{}`)))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sphere/wallet-products/catalog", nil))
	if w.Code != 404 {
		t.Fatal("cross-service route", w.Code)
	}
	empty, err := publicCatalogs(Config{})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	empty.ServeHTTP(w, httptest.NewRequest("GET", "/api/wallet-products/catalog", nil))
	if w.Code != 503 {
		t.Fatal("missing explicit product currency", w.Code)
	}
}
