// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/progression"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
)

// modules holds in-process domain handlers. There is no reverse proxy, service
// discovery, gRPC schema, or network hop in this independently authored host.
type modules struct{ board, taxonomy, posts, messager, wallet, accounts, session, public, interactions, social, media, notifications, realtime, apps, commerce, signin, timeline, surveys, stickers, locations, presence, progression http.Handler }
type router struct{ modules }

func under(p, base string) bool { return p == base || strings.HasPrefix(p, base+"/") }
func (h router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w = headWriter{w}
	}
	p := r.URL.Path
	// Reject alternate path encodings instead of redirecting a mutating request
	// between services. Neither a service-name prefix nor traversal is authority.
	if p == "" || p[0] != '/' || path.Clean(p) != p || strings.Contains(p, "\\") || strings.Contains(p, "//") || r.URL.RawPath != "" && r.URL.EscapedPath() != p {
		problem(w, 400, "INVALID_ROUTE_PATH")
		return
	}
	if h.signin != nil && signinRoute(r.Method, p) {
		h.signin.ServeHTTP(w, r)
		return
	}
	if h.realtime != nil && (p == "/ws" || under(p, "/api/realtime")) {
		h.realtime.ServeHTTP(w, r)
		return
	}
	if h.public != nil && publicCatalogRoute(p) {
		h.public.ServeHTTP(w, r)
		return
	}
	if p == "/healthz" {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			problem(w, 405, "METHOD_NOT_ALLOWED")
			return
		}
		problem(w, 200, "BACKEND_ALIVE")
		return
	}
	{
		canonical := p
		for _, prefix := range []string{"/passport/", "/stargate/"} {
			if strings.HasPrefix(p, prefix) {
				canonical = "/api/" + strings.TrimPrefix(p, prefix)
			}
		}
		var handler http.Handler
		if presence.RouteMatches(canonical) {
			handler = h.presence
		}
		if progression.RouteMatches(canonical) {
			handler = h.progression
		}
		if handler != nil {
			copy := r.Clone(r.Context())
			u := *r.URL
			u.Path = canonical
			copy.URL = &u
			handler.ServeHTTP(w, copy)
			return
		}
	}
	if h.locations != nil {
		canonical := p
		for _, prefix := range []string{"/passport/", "/sphere/"} {
			if strings.HasPrefix(p, prefix) {
				canonical = "/api/" + strings.TrimPrefix(p, prefix)
			}
		}
		if under(canonical, "/api/meets") || under(canonical, "/api/pins") || under(canonical, "/api/nfc") {
			copy := r.Clone(r.Context())
			u := *r.URL
			u.Path = canonical
			copy.URL = &u
			h.locations.ServeHTTP(w, copy)
			return
		}
	}
	service, rest, found := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	var target http.Handler
	canonical := p
	switch service {
	case "passport":
		target = h.board
		if found {
			canonical = "/api/" + rest
		} else {
			canonical = "/api"
		}
	case "develop":
		target = h.board
		if found {
			canonical = "/api/" + rest
		} else {
			canonical = "/api"
		}
		if !under(canonical, "/api/private/apps") {
			target = nil
		}
	case "sphere":
		target = h.taxonomy
		if found {
			canonical = "/api/" + rest
		} else {
			canonical = "/api"
		}
	case "messager":
		target = h.messager
		if found {
			canonical = "/api/" + rest
		} else {
			canonical = "/api"
		}
	case "drive":
		target = h.media
		if found {
			canonical = "/api/" + rest
		} else {
			canonical = "/api"
		}
	case "wallet":
		target = h.wallet
		if found {
			canonical = "/api/" + rest
		} else {
			canonical = "/api"
		}
	case "stargate":
		if p == "/stargate/accounts/me/pin-status" {
			target = h.wallet
			canonical = "/api/accounts/me/pin-status"
		} else if under(p, "/stargate/accounts") {
			target = h.accounts
		} else {
			target = h.session
		}
	case "api":
		switch {
		case under(p, "/api/wallets"), p == "/api/accounts/me/pin-status":
			target = h.wallet
		case under(p, "/api/chat"):
			target = h.messager
		case taxonomyRoute(p):
			target = h.taxonomy
		case under(p, "/api/posts"):
			target = h.posts
		case boardRoute(p):
			target = h.board
		case under(p, "/api/accounts"), under(p, "/api/publishers"):
			target = h.accounts
		case under(p, "/api/sessions"), under(p, "/api/auth"):
			target = h.session
			canonical = "/stargate/" + strings.TrimPrefix(p, "/api/")
		}
	}
	if (service == "passport" || service == "develop") && !boardRoute(canonical) {
		target = nil
	}
	if service == "sphere" && !taxonomyRoute(canonical) {
		target = nil
		if under(canonical, "/api/posts") {
			target = h.posts
		}
	}
	if h.accounts != nil && (under(canonical, "/api/publishers") || under(canonical, "/api/accounts") && !boardRoute(canonical) && canonical != "/api/accounts/me/pin-status") && (service == "sphere" || service == "passport" || service == "api") {
		target = h.accounts
	}
	if h.notifications != nil && (under(canonical, "/api/notifications") || under(canonical, "/metoer/notifications")) && (service == "api" || service == "metoer") {
		target = h.notifications
	}
	if h.media != nil && (under(canonical, "/api/media") || under(canonical, "/api/files")) && (service == "drive" || service == "api") {
		target = h.media
	}
	if h.interactions != nil && interactionRoute(canonical) && (service == "sphere" || service == "api") {
		target = h.interactions
	}
	if h.social != nil && socialRoute(canonical) && (service == "passport" || service == "stargate" || service == "api") {
		target = h.social
	}
	if h.public != nil && publicCatalogRoute(canonical) && (service == "wallet" || service == "passport" || service == "api") {
		target = h.public
	}
	if h.commerce != nil && (under(canonical, "/api/orders") || under(canonical, "/api/subscriptions")) && (service == "wallet" || service == "api") {
		target = h.commerce
	}
	if h.apps != nil && appRoute(canonical) && (service == "develop" || service == "api") {
		target = h.apps
	}
	if under(canonical, "/api/private/apps") && (h.apps == nil || (service != "develop" && service != "api")) {
		problem(w, 501, "APP_IDENTITY_UNSUPPORTED")
		return
	}
	if service == "sphere" || service == "api" {
		if h.surveys != nil && under(canonical, "/api/surveys") {
			target = h.surveys
		}
		if h.stickers != nil && under(canonical, "/api/stickers") {
			target = h.stickers
		}
	}
	if h.timeline != nil && (service == "sphere" || service == "api") {
		probe := r.Clone(r.Context())
		u := *r.URL
		u.Path = canonical
		probe.URL = &u
		if timeline.Handles(probe) {
			target = h.timeline
		}
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Path = canonical
	u.RawPath = ""
	copy.URL = &u
	// Keep the original RequestURI for logging by the ingress; handlers consume URL.
	target.ServeHTTP(locationWriter{ResponseWriter: w, canonicalPrefix: "/api", externalPrefix: externalPrefix(service)}, copy)
}
func boardRoute(p string) bool {
	return (under(p, "/api/accounts") || under(p, "/api/admin/accounts")) && (strings.HasSuffix(p, "/board") || strings.Contains(p, "/board/items/")) || under(p, "/api/private/apps") && strings.Contains(p, "/board/")
}
func taxonomyRoute(p string) bool {
	for _, base := range []string{"/api/posts/tags", "/api/posts/categories", "/api/admin/posts/tags", "/api/admin/tags", "/api/admin/categories", "/api/categories/subscriptions"} {
		if under(p, base) {
			return true
		}
	}
	return false
}
func externalPrefix(service string) string {
	switch service {
	case "passport", "develop", "sphere", "messager", "wallet":
		return "/" + service
	}
	return "/api"
}

// Location remains usable through the same gateway prefix used by the caller.
// External URLs and unrelated locations are never rewritten.
type locationWriter struct {
	http.ResponseWriter
	canonicalPrefix, externalPrefix string
}

func (w locationWriter) WriteHeader(status int) {
	if raw := w.Header().Get("Location"); raw != "" && w.externalPrefix != w.canonicalPrefix {
		if u, err := url.Parse(raw); err == nil && !u.IsAbs() && u.Host == "" && under(u.Path, w.canonicalPrefix) {
			u.Path = w.externalPrefix + strings.TrimPrefix(u.Path, w.canonicalPrefix)
			u.RawPath = ""
			w.Header().Set("Location", u.String())
		}
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w locationWriter) Write(b []byte) (int, error) { return w.ResponseWriter.Write(b) }

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(b []byte) (int, error) { return len(b), nil }

func interactionRoute(p string) bool {
	if under(p, "/api/collections") || p == "/api/posts/bookmarks" {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	return len(parts) == 4 && parts[0] == "api" && parts[1] == "posts" && (parts[3] == "reactions" || parts[3] == "bookmark") || len(parts) >= 4 && parts[0] == "api" && parts[1] == "publishers" && parts[3] == "collections"
}
func socialRoute(p string) bool {
	return under(p, "/api/relationships") || under(p, "/stargate/relationships") || under(p, "/api/realms")
}

func appRoute(p string) bool {
	return under(p, "/api/developers") || under(p, "/api/private/projects") || under(p, "/api/private/apps") || under(p, "/api/apps")
}

// Credential protocol endpoints perform their own factor/session verification;
// their bearer input must reach that verifier, rather than the domain adapter.
func signinRoute(method, p string) bool {
	return method == "POST" && p == "/stargate/accounts" || p == "/stargate/accounts/verify" || p == "/stargate/accounts/verification" || under(p, "/stargate/auth/challenge") || under(p, "/stargate/factors") || p == "/stargate/accounts/me/password" || p == "/passport/accounts/recovery/password"
}
