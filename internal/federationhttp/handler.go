// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

type Handler struct {
	Repository f.Repository
	Config     f.Config
	Keys       f.KeyProvider
	Authority  f.Authority
}

func IsPublicRoute(p string) bool {
	return IsQuoteRoute(p) || p == "/.well-known/webfinger" || p == "/.well-known/host-meta" || strings.HasPrefix(p, "/activitypub/")
}
func IsPrivateRoute(p string) bool {
	return IsQuoteRoute(p) || p == "/api/activitypub" || strings.HasPrefix(p, "/api/activitypub/") || strings.HasPrefix(p, "/api/fediverse/") || strings.HasPrefix(p, "/api/admin/fediverse/")
}
func write(w http.ResponseWriter, status int, value any, media string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if status == 204 || status == 202 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, e error) {
	var x *f.Failure
	if errors.As(e, &x) {
		write(w, x.Status, x, "application/json")
		return
	}
	var a *authority.Failure
	if errors.As(e, &a) {
		write(w, a.Status, a, "application/json")
		return
	}
	write(w, 503, f.ErrUnavailable, "application/json")
}
func method(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	write(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"}, "application/json")
}
func page(r *http.Request) (int, int, error) {
	offset, take := 0, 20
	for k := range r.URL.Query() {
		if k != "offset" && k != "take" {
			return 0, 0, f.ErrInvalid
		}
	}
	if len(r.URL.Query()["offset"]) > 1 || len(r.URL.Query()["take"]) > 1 {
		return 0, 0, f.ErrInvalid
	}
	var e error
	if s := r.URL.Query().Get("offset"); s != "" {
		offset, e = strconv.Atoi(s)
		if e != nil {
			return 0, 0, f.ErrInvalid
		}
	}
	if s := r.URL.Query().Get("take"); s != "" {
		take, e = strconv.Atoi(s)
		if e != nil {
			return 0, 0, f.ErrInvalid
		}
	}
	if offset < 0 || offset > 10000 || take < 1 || take > 100 {
		return 0, 0, f.ErrInvalid
	}
	return offset, take, nil
}
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, f.ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, f.ErrInvalid
	}
	if e = f.CheckJSON(b); e != nil {
		return nil, e
	}
	return b, nil
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	b, e := readBody(w, r, 16384)
	if e != nil {
		return e
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return f.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return f.ErrInvalid
	}
	return nil
}
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Repository == nil || h.Config.Validate() != nil {
		fail(w, f.ErrUnavailable)
		return
	}
	p := r.URL.Path
	if path.Clean(p) != p || strings.ContainsAny(p, "\\") || strings.Contains(p, "//") || r.URL.RawPath != "" || r.URL.User != nil {
		fail(w, f.ErrInvalid)
		return
	}
	for k := range r.Header {
		key := strings.ToLower(k)
		if key == "x-account-id" || key == "x-user-id" || key == "x-role" || key == "proxy-authorization" || strings.HasPrefix(key, "x-test-") || strings.HasPrefix(key, "x-synthetic-") {
			fail(w, f.ErrForbidden)
			return
		}
	}
	if IsQuoteRoute(p) {
		h.quote(w, r)
		return
	}
	if p == "/.well-known/host-meta" {
		h.hostMeta(w, r)
		return
	}
	if p == "/activitypub/actor" || strings.HasPrefix(p, "/activitypub/actor/") {
		h.serverActor(w, r)
		return
	}
	if strings.HasPrefix(p, "/activitypub/realms/") {
		h.realm(w, r)
		return
	}
	if strings.HasPrefix(p, "/api/fediverse/") || strings.HasPrefix(p, "/api/admin/fediverse/") {
		h.fediverse(w, r)
		return
	}
	if IsPrivateRoute(p) {
		h.private(w, r)
		return
	}
	if p == "/.well-known/webfinger" {
		h.webfinger(w, r)
		return
	}
	if strings.HasPrefix(p, "/activitypub/objects/") {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		id := strings.TrimPrefix(p, "/activitypub/objects/")
		n, e := h.Repository.Object(r.Context(), id)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, n, f.MediaType)
		return
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "activitypub" || parts[1] != "actors" || !f.ValidName(parts[2]) {
		http.NotFound(w, r)
		return
	}
	name := parts[2]
	if len(parts) == 4 && parts[3] == "inbox" {
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		media, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || (media != f.MediaType && media != "application/ld+json") || media == "application/ld+json" && params["profile"] != "" && params["profile"] != f.Context {
			write(w, 406, map[string]string{"code": "ACTIVITYPUB_INVALID_CONTENT_TYPE"}, "application/json")
			return
		}
		b, e := readBody(w, r, f.MaxBody)
		if e != nil {
			fail(w, e)
			return
		}
		verified, e := f.Verify(r.Context(), h.Config, h.Keys, r, b)
		if e != nil {
			fail(w, e)
			return
		}
		if e = h.Repository.Receive(r.Context(), name, verified); e != nil {
			fail(w, e)
			return
		}
		write(w, 202, nil, f.MediaType)
		return
	}
	if r.Method != "GET" {
		method(w, "GET")
		return
	}
	if len(parts) == 3 {
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		a, e := h.Repository.Actor(r.Context(), name)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, a, f.MediaType)
		return
	}
	if parts[3] == "main-key" || parts[3] == "publickey" {
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		a, e := h.Repository.Actor(r.Context(), name)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, f.PublicKeyDocument(a.PublicKey), f.MediaType)
		return
	}
	offset, take, e := page(r)
	if e != nil {
		fail(w, e)
		return
	}
	var c f.Collection
	switch parts[3] {
	case "outbox":
		c, e = h.Repository.Outbox(r.Context(), name, offset, take)
		if e == nil && r.URL.RawQuery == "" {
			c = f.Collection{Context: f.Context, ID: c.PartOf, Type: "OrderedCollection", Total: c.Total, First: c.PartOf + "?offset=0&take=20"}
		}
	case "followers", "following":
		c, e = h.Repository.Collection(r.Context(), name, parts[3], offset, take)
	case "featured":
		repo, ok := h.Repository.(f.FeaturedRepository)
		if !ok {
			e = f.ErrUnavailable
		} else {
			c, e = repo.Featured(r.Context(), name, offset, take)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, c, f.MediaType)
}
func (h Handler) webfinger(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		method(w, "GET")
		return
	}
	q := r.URL.Query()
	if len(q) != 1 || len(q["resource"]) != 1 {
		fail(w, f.ErrInvalid)
		return
	}
	resource := q.Get("resource")
	if !strings.HasPrefix(resource, "acct:") {
		fail(w, f.ErrInvalid)
		return
	}
	name, domain, ok := strings.Cut(strings.TrimPrefix(resource, "acct:"), "@")
	if !ok || !f.ValidName(name) || strings.Contains(domain, "@") {
		fail(w, f.ErrInvalid)
		return
	}
	if "https://"+domain != h.Config.Origin {
		fail(w, f.ErrNotFound)
		return
	}
	if repo, ok := h.Repository.(f.ServerRepository); ok {
		a, e := repo.ServerActor(r.Context())
		if e == nil && strings.EqualFold(name, a.Username) {
			write(w, 200, map[string]any{"subject": "acct:" + a.Username + "@" + domain, "links": []map[string]string{{"rel": "self", "type": f.MediaType, "href": a.ID}, {"rel": "http://webfinger.net/rel/profile-page", "type": "text/html", "href": h.Config.Origin}}}, "application/jrd+json")
			return
		}
	}
	a, e := h.Repository.Actor(r.Context(), name)
	if e != nil {
		fail(w, e)
		return
	}
	response := map[string]any{"subject": "acct:" + a.Username + "@" + domain, "links": []map[string]string{{"rel": "self", "type": f.MediaType, "href": a.ID}}}
	if name != a.Username {
		response["aliases"] = []string{resource}
	}
	write(w, 200, response, "application/jrd+json")
}
func (h Handler) private(w http.ResponseWriter, r *http.Request) {
	if h.Authority == nil {
		fail(w, authority.ErrUnavailable)
		return
	}
	p, e := h.Authority.AuthenticateRequest(r)
	if e != nil {
		fail(w, e)
		return
	}
	permission := f.Permission
	if strings.HasPrefix(r.URL.Path, "/api/activitypub/realms/") {
		permission = f.RealmPermission
	}
	p, e = h.Authority.AuthorizePrincipal(r.Context(), p, permission)
	if e != nil {
		fail(w, e)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "activitypub" && parts[2] == "realms" && authority.ValidID(parts[3]) && parts[4] == "settings" {
		if r.Method != "PATCH" {
			method(w, "PATCH")
			return
		}
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if r.URL.RawQuery != "" || decode(w, r, &input) != nil || input.Enabled == nil {
			fail(w, f.ErrInvalid)
			return
		}
		repo, ok := h.Repository.(f.RealmAdminRepository)
		if !ok {
			fail(w, f.ErrUnavailable)
			return
		}
		if e = repo.SetRealmFederation(r.Context(), p.Account.ID, parts[3], *input.Enabled); e != nil {
			fail(w, e)
			return
		}
		write(w, 204, nil, "application/json")
		return
	}
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "activitypub" || parts[2] != "publishers" || !authority.ValidID(parts[3]) {
		http.NotFound(w, r)
		return
	}
	ref := parts[3]
	switch parts[4] {
	case "public-admissions":
		h.publicAdmission(w, r, p.Account.ID, ref)
		return
	case "discovery":
		h.discovery(w, r, p.Account.ID, ref)
		return
	case "settings":
		if r.Method != "PATCH" {
			method(w, "PATCH")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		var input struct {
			Enabled *bool `json:"enabled"`
			Manual  *bool `json:"manually_approves_followers"`
		}
		if e = decode(w, r, &input); e != nil || input.Enabled == nil || input.Manual == nil {
			fail(w, f.ErrInvalid)
			return
		}
		if e = h.Repository.Settings(r.Context(), p.Account.ID, ref, f.Settings{Enabled: *input.Enabled, Manual: *input.Manual}); e != nil {
			fail(w, e)
			return
		}
		write(w, 204, nil, "application/json")
	case "featured":
		if r.Method != "PUT" {
			method(w, "PUT")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		var input struct {
			PostIDs []string `json:"post_ids"`
		}
		if decode(w, r, &input) != nil {
			fail(w, f.ErrInvalid)
			return
		}
		repo, ok := h.Repository.(f.FeaturedRepository)
		if !ok {
			fail(w, f.ErrUnavailable)
			return
		}
		if e = repo.SetFeatured(r.Context(), p.Account.ID, ref, input.PostIDs); e != nil {
			fail(w, e)
			return
		}
		write(w, 204, nil, "application/json")
	case "relationships":
		if r.Method == "GET" {
			offset, take, e := page(r)
			if e != nil {
				fail(w, e)
				return
			}
			rows, e := h.Repository.Relationships(r.Context(), p.Account.ID, ref, offset, take)
			if e != nil {
				fail(w, e)
				return
			}
			write(w, 200, rows, "application/json")
			return
		}
		if r.Method != "POST" {
			method(w, "GET, POST")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		var c f.Change
		if e = decode(w, r, &c); e != nil {
			fail(w, e)
			return
		}
		if e = h.Repository.Change(r.Context(), p.Account.ID, ref, c); e != nil {
			fail(w, e)
			return
		}
		write(w, 204, nil, "application/json")
	default:
		http.NotFound(w, r)
	}
}
