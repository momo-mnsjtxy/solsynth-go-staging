// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from DysonNetwork at 4ee2d0cebb9da8284a92483fee6dce795bf6947c.
// Modified 2026-10-08. HTTP routing/errors are candidates, not oracle-verified.
package nodeinfo

import (
	"encoding/json"
	"net/http"
	"strings"
)

type handler struct {
	reader    Reader
	domain    *string
	candidate bool
}

// NewHandler denies every request without reading storage. Upstream global
// middleware and Shared policy have not been established from licensed sources.
func NewHandler(reader Reader, domain *string) http.Handler {
	return newHandler(reader, domain, false)
}

// NewSyntheticCompatibilityHandler explicitly exposes the four source GET
// paths for isolated development. It rejects identity-bearing requests. HEAD,
// trailing slashes, error statuses/bodies and content type are candidate choices.
// Discovery does not query storage, matching its source method.
func NewSyntheticCompatibilityHandler(reader Reader, domain *string) http.Handler {
	return newHandler(reader, domain, true)
}

func newHandler(reader Reader, domain *string, candidate bool) http.Handler {
	h := handler{reader: reader, candidate: candidate}
	if domain != nil {
		value := *domain
		h.domain = &value
	}
	return h
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		// net/http suppresses HEAD response bodies on the wire; keep recorder
		// tests and direct-handler calls consistent with that candidate choice.
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(struct {
				Error string `json:"error"`
			}{code})
		}
	}
	if !h.candidate {
		fail(http.StatusServiceUnavailable, "nodeinfo_policy_unverified")
		return
	}
	switch r.URL.Path {
	case "/.well-known/host-meta", "/.well-known/nodeinfo", "/.well-known/nodeinfo/2.0", "/.well-known/nodeinfo/2.1":
	default:
		fail(http.StatusNotFound, "nodeinfo_route_not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(http.StatusMethodNotAllowed, "nodeinfo_method_not_allowed")
		return
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "X-Test-Account") {
			fail(http.StatusForbidden, "nodeinfo_identity_unsupported")
			return
		}
	}
	if r.Context().Err() != nil {
		fail(http.StatusServiceUnavailable, "nodeinfo_unavailable")
		return
	}
	if r.URL.Path == "/.well-known/host-meta" {
		w.Header().Set("Content-Type", "application/xrd+xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(HostMeta(h.domain))
		return
	}
	var result any
	if r.URL.Path == "/.well-known/nodeinfo" {
		result = Discover(h.domain)
	} else {
		if h.reader == nil {
			fail(http.StatusServiceUnavailable, "nodeinfo_unavailable")
			return
		}
		stats, err := h.reader.ReadStats(r.Context())
		if err != nil || r.Context().Err() != nil || stats.TotalPublishers < 0 || stats.TotalPosts < 0 || stats.ActivePublishers < 0 {
			fail(http.StatusServiceUnavailable, "nodeinfo_unavailable")
			return
		}
		if r.URL.Path == "/.well-known/nodeinfo/2.0" {
			result = Version20(stats)
		} else {
			result = Version21(stats)
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		fail(http.StatusServiceUnavailable, "nodeinfo_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(data, '\n'))
}
