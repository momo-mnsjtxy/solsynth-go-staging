// SPDX-License-Identifier: AGPL-3.0-only
package productcatalog

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductCatalogHTTPGatesAndMethods(t *testing.T) {
	catalog, err := NewWithSyntheticDefaultCurrency("golds")
	if err != nil {
		t.Fatal(err)
	}
	denied := NewHandler(catalog)
	r := httptest.NewRequest("GET", "/api/wallet-products/catalog", nil)
	w := httptest.NewRecorder()
	denied.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	h := NewSyntheticCompatibilityHandler(catalog)
	for _, path := range []string{"/api/wallet-products/catalog", "/API/WALLET-PRODUCTS/CATALOG/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"currency":"golds"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"HEAD", "POST", "PUT"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/wallet-products/catalog", nil))
		if w.Code != 405 || w.Header().Get("Allow") != "GET" || w.Body.Len() != 0 {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account"} {
		r := httptest.NewRequest("GET", "/api/wallet-products/catalog", nil)
		r.Header.Set(header, "")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(header, w.Code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(ctx))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"/api/wallet-products/catalog//", "/api/wallet-productſ/catalog", "/api/wallet-products/golds-resupply-pack/checkout/paddle"} {
		if RouteMatches(path) {
			t.Fatal(path)
		}
	}
}
