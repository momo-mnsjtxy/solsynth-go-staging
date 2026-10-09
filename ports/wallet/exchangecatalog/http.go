// SPDX-License-Identifier: AGPL-3.0-only
package exchangecatalog

import (
	"encoding/json"
	"net/http"
	"strings"
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// RouteMatches follows the verified MVC route's case-insensitive spelling and
// optional single trailing slash. Method matching remains GET only.
func RouteMatches(path string) bool {
	return equalOrdinal(strings.TrimSuffix(path, "/"), "/api/wallets/exchange")
}

// NewHandler exposes only the anonymous catalog, never the POST funds exchange.
// Optional diagnostic fields described by SHARED_API_ERRORS.md are omitted.
func NewHandler(catalog *Catalog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !RouteMatches(r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		options, err := catalog.Options()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(apiError{Code: "WALLET_EXCHANGE_CONFIGURATION_INVALID", Message: "Currency exchange configuration is invalid.", Status: 500})
			return
		}
		_ = json.NewEncoder(w).Encode(options)
	})
}
