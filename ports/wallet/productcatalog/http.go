// SPDX-License-Identifier: AGPL-3.0-only
package productcatalog

import (
	"net/http"
	"strings"
)

type handler struct {
	catalog   *Catalog
	candidate bool
}

func RouteMatches(path string) bool {
	path = strings.TrimSuffix(path, "/")
	const expected = "/api/wallet-products/catalog"
	if len(path) != len(expected) {
		return false
	}
	for i := range len(expected) {
		c := path[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != expected[i] {
			return false
		}
	}
	return true
}

// NewHandler stays disabled until whole-host global policy is established.
func NewHandler(catalog *Catalog) http.Handler { return handler{catalog: catalog} }

// NewOwnedPublicHandler applies the independent anonymous-read policy to explicit
// catalog configuration. Hosts must validate supplied identity before dispatch.
// Raw identity-bearing requests remain rejected by this handler.
func NewOwnedPublicHandler(catalog *Catalog) http.Handler {
	return handler{catalog: catalog, candidate: true}
}
func NewSyntheticCompatibilityHandler(catalog *Catalog) http.Handler {
	return handler{catalog: catalog, candidate: true}
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
		}
	}
	if !h.candidate {
		fail(503, "wallet_product_policy_unverified")
		return
	}
	if !RouteMatches(r.URL.Path) {
		w.WriteHeader(404)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		return
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "X-Test-Account") {
			fail(403, "wallet_product_identity_unsupported")
			return
		}
	}
	if r.Context().Err() != nil {
		fail(503, "wallet_product_unavailable")
		return
	}
	data, err := h.catalog.JSON()
	if err != nil {
		fail(503, "wallet_product_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
