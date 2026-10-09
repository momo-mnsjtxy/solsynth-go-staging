// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/internal/federationhttp"
	"solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/passport/accountsettings"
	"solsynth.local/go-migration/ports/passport/ipcheck"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/progression"
	"solsynth.local/go-migration/ports/passport/realmboosts"
	"solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/rewind"
	"solsynth.local/go-migration/ports/sphere/autocomplete"
	"solsynth.local/go-migration/ports/sphere/blogshttp"
	"solsynth.local/go-migration/ports/sphere/livestreams"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
)

// modules holds in-process domain handlers. There is no reverse proxy, service
// discovery, gRPC schema, or network hop in this independently authored host.
type modules struct {
	realmMemberHistory                                                                                                                                                                                                                                   bool
	blogs                                                                                                                                                                                                                                                http.Handler
	realmMembers                                                                                                                                                                                                                                         http.Handler
	realmBoosts                                                                                                                                                                                                                                          http.Handler
	affiliations                                                                                                                                                                                                                                         http.Handler
	artwork                                                                                                                                                                                                                                              http.Handler
	passkit                                                                                                                                                                                                                                              passkitModules
	publisherCommerce                                                                                                                                                                                                                                    publisherCommerceModules
	domainCollections                                                                                                                                                                                                                                    domainCollectionModules
	admin                                                                                                                                                                                                                                                adminModules
	livestreams                                                                                                                                                                                                                                          http.Handler
	moderation                                                                                                                                                                                                                                           http.Handler
	namecards, rewind                                                                                                                                                                                                                                    http.Handler
	ipcheck                                                                                                                                                                                                                                              http.Handler
	accountSettings                                                                                                                                                                                                                                      http.Handler
	translation, autocomplete                                                                                                                                                                                                                            http.Handler
	reader                                                                                                                                                                                                                                               http.Handler
	oauth                                                                                                                                                                                                                                                http.Handler
	fortunes                                                                                                                                                                                                                                             http.Handler
	calls, federationPublic, federationPrivate                                                                                                                                                                                                           http.Handler
	board, taxonomy, posts, messager, wallet, accounts, session, public, interactions, social, media, notifications, realtime, apps, commerce, signin, timeline, surveys, stickers, locations, presence, progression, merchants, tickets, bots, calendar http.Handler
}
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
	if realmmembers.HandlesRequest(r) && (!realmmembers.ExperienceRoute(p) || h.realmMemberHistory) {
		if h.realmMembers == nil {
			problem(w, 503, "REALM_MEMBER_UNAVAILABLE")
			return
		}
		h.realmMembers.ServeHTTP(w, r)
		return
	}
	if realmboosts.Handles(p) {
		if h.realmBoosts == nil {
			problem(w, 503, "REALM_BOOST_UNAVAILABLE")
			return
		}
		h.realmBoosts.ServeHTTP(w, r)
		return
	}
	if h.passkit.route(w, r) {
		return
	}
	if handler := h.publisherCommerce.route(p); handler != nil {
		handler.ServeHTTP(w, r)
		return
	}
	if handler := h.domainCollections.route(p); handler != nil {
		handler.ServeHTTP(w, r)
		return
	}
	// Federation signatures cover the original canonical target. These public
	// routes bypass bearer middleware and gateway path rewriting.
	if h.federationPrivate != nil && federationhttp.IsQuoteRoute(p) && r.Method != "GET" {
		h.federationPrivate.ServeHTTP(w, r)
		return
	}
	if h.federationPublic != nil && federationhttp.IsPublicRoute(p) {
		h.federationPublic.ServeHTTP(w, r)
		return
	}
	if h.federationPrivate != nil && federationhttp.IsPrivateRoute(p) {
		h.federationPrivate.ServeHTTP(w, r)
		return
	}
	if h.oauth != nil && oauth.RouteMatches(p) {
		h.oauth.ServeHTTP(w, r)
		return
	}
	if h.signin != nil && (signinRoute(r.Method, p) || signin.ContactRouteMatches(p)) {
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
	if h.affiliations != nil && (under(p, "/api/affiliations") || under(p, "/passport/affiliations")) {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Path = strings.Replace(p, "/passport/", "/api/", 1)
		copy.URL = &u
		h.affiliations.ServeHTTP(w, copy)
		return
	}
	if h.tickets != nil && (p == "/api/accounts/me/activation/progress" || p == "/passport/accounts/me/activation/progress") {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Path = "/api/tests/activation"
		copy.URL = &u
		h.tickets.ServeHTTP(w, copy)
		return
	}
	if h.artwork != nil && under(p, "/api/presence/artworks") {
		h.artwork.ServeHTTP(w, r)
		return
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
	if h.fortunes != nil {
		canonical := p
		if strings.HasPrefix(p, "/passport/") {
			canonical = "/api/" + strings.TrimPrefix(p, "/passport/")
		}
		if canonical == "/api/fortune" || canonical == "/api/fortune/random" {
			copy := r.Clone(r.Context())
			u := *r.URL
			u.Path = canonical
			copy.URL = &u
			h.fortunes.ServeHTTP(w, copy)
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
	case "e2ee":
		if under(p, "/e2ee/mls") {
			target = h.messager
		}
	case "stargate":
		if under(p, "/stargate/e2ee/mls") {
			target = h.messager
			canonical = strings.TrimPrefix(p, "/stargate")
		} else if p == "/stargate/accounts/me/pin-status" {
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
		case under(p, "/api/e2ee/mls"):
			target = h.messager
			canonical = strings.TrimPrefix(p, "/api")
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
	if h.blogs != nil && (service == "sphere" || service == "api") && blogshttp.Handles(canonical) {
		target = h.blogs
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
	if h.messager != nil && (service == "api" || service == "messager") {
		parts := strings.Split(strings.Trim(canonical, "/"), "/")
		if len(parts) == 4 && parts[0] == "api" && parts[1] == "realms" && parts[2] != "" && parts[3] == "chat" {
			target = h.messager
		}
	}
	if h.public != nil && publicCatalogRoute(canonical) && (service == "wallet" || service == "passport" || service == "api") {
		target = h.public
	}
	if h.accountSettings != nil && (service == "api" || service == "passport") && accountsettings.RouteMatches(canonical) {
		target = h.accountSettings
	}
	if service == "api" || service == "passport" || service == "stargate" {
		if h.namecards != nil && namecards.RouteMatches(canonical) {
			target = h.namecards
		}
		if h.rewind != nil && rewind.RouteMatches(canonical) {
			target = h.rewind
		}
	}
	if h.moderation != nil && content.Handles(canonical) && (service == "api" || service == "passport" && under(canonical, "/api/admin/realms") || service == "sphere" && !under(canonical, "/api/admin/realms")) {
		target = h.moderation
	}
	if h.livestreams != nil && (service == "api" || service == "sphere") && livestreams.RouteMatches(canonical) {
		target = h.livestreams
	}
	if admin := h.admin.route(service, canonical); admin != nil {
		target = admin
	}
	if h.ipcheck != nil && (service == "api" || service == "passport") && ipcheck.Route(canonical) {
		target = h.ipcheck
	}
	if h.translation != nil && (service == "api" || service == "sphere") && canonical == "/api/translate" {
		target = h.translation
	}
	if h.autocomplete != nil {
		if room, ok := autocomplete.Route(canonical); ok && (service == "api" || room == "" && service == "sphere" || room != "" && service == "messager") {
			target = h.autocomplete
		}
	}
	if h.reader != nil && (service == "api" || service == "sphere") && under(canonical, "/api/scrap") {
		target = h.reader
	}
	if h.calendar != nil && (service == "api" || service == "passport") && calendarRoute(canonical) {
		target = h.calendar
	}
	if h.bots != nil && (service == "api" || service == "develop" && !bots.RoomCommandsRoute(canonical) || service == "messager" && bots.RoomCommandsRoute(canonical)) && bots.RouteMatches(canonical) {
		target = h.bots
	}
	if h.tickets != nil && (service == "api" || service == "passport") && ticketRoute(canonical) {
		target = h.tickets
	}
	if h.merchants != nil && (service == "api" || service == "wallet") && (under(canonical, "/api/merchants") || under(canonical, "/api/admin/subscriptions/catalog")) {
		target = h.merchants
	}
	if h.commerce != nil && (under(canonical, "/api/orders") || under(canonical, "/api/subscriptions") || under(canonical, "/api/coupons")) && (service == "wallet" || service == "api") {
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
	if h.calls != nil && (service == "api" || service == "messager") && under(canonical, "/api/chat/realtime") {
		target = h.calls
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

func ticketRoute(p string) bool {
	for _, base := range []string{"/api/tickets", "/api/tests", "/api/admin/tickets/on-call", "/api/admin/tests", "/api/admin/test-question-groups", "/api/admin/test-questions", "/api/admin/test-trials"} {
		if under(p, base) {
			return true
		}
	}
	return false
}

func calendarRoute(p string) bool {
	if under(p, "/api/notable-days") {
		return true
	}
	v := strings.Split(strings.Trim(p, "/"), "/")
	return len(v) >= 4 && v[0] == "api" && v[1] == "accounts" && v[3] == "calendar"
}
