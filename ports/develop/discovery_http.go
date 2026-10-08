// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from Solsynth/DysonNetwork, revision
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-07.
package develop

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type discoveryHandler struct {
	reader    Reader
	candidate bool
}

// NewHandler refuses every request without querying storage. The retained
// startup uses global permission middleware whose policy remains unverified.
func NewHandler(reader Reader) http.Handler { return discoveryHandler{reader: reader} }

// NewSyntheticCompatibilityHandler exposes only GET /api/apps for isolated
// candidate tests. It rejects identity-bearing requests and is not a production
// authentication implementation. Default pagination is 20/0; candidate bounds
// are 0<=take<=100 and offset>=0. Duplicate query values are rejected instead of
// guessing upstream model binding. No existing ApiError parity is claimed.
func NewSyntheticCompatibilityHandler(reader Reader) http.Handler {
	return discoveryHandler{reader: reader, candidate: true}
}

func (h discoveryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Error string `json:"error"`
		}{code})
	}
	if !h.candidate {
		fail(http.StatusServiceUnavailable, "discovery_policy_unverified")
		return
	}
	if r.URL.Path != "/api/apps" {
		fail(http.StatusNotFound, "discovery_route_not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(http.StatusMethodNotAllowed, "discovery_method_not_allowed")
		return
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "X-Test-Account") {
			fail(http.StatusForbidden, "discovery_identity_unsupported")
			return
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(http.StatusBadRequest, "discovery_query_invalid")
		return
	}
	q := Query{Take: 20}
	for _, param := range []struct {
		name string
		out  *int32
	}{{"take", &q.Take}, {"offset", &q.Offset}} {
		if raw, ok := values[param.name]; ok {
			if len(raw) != 1 {
				fail(http.StatusBadRequest, "discovery_query_invalid")
				return
			}
			n, err := strconv.ParseInt(raw[0], 10, 32)
			if err != nil {
				fail(http.StatusBadRequest, "discovery_query_invalid")
				return
			}
			*param.out = int32(n)
		}
	}
	if raw, ok := values["search"]; ok {
		if len(raw) != 1 {
			fail(http.StatusBadRequest, "discovery_query_invalid")
			return
		}
		q.Search = &raw[0]
	}
	if ValidateQuery(q) != nil {
		fail(http.StatusBadRequest, "discovery_query_invalid")
		return
	}
	if h.reader == nil || r.Context().Err() != nil {
		fail(http.StatusServiceUnavailable, "discovery_unavailable")
		return
	}
	page, err := h.reader.Discover(r.Context(), q)
	if err != nil || r.Context().Err() != nil || page.Total < 0 {
		fail(http.StatusServiceUnavailable, "discovery_unavailable")
		return
	}
	if page.Apps == nil {
		page.Apps = []AppDiscovery{}
	}
	data, err := json.Marshal(page.Apps)
	if err != nil {
		fail(http.StatusServiceUnavailable, "discovery_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Total", strconv.FormatInt(int64(page.Total), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(data, '\n'))
}
