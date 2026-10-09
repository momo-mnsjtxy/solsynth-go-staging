// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"mime"
	"net/http"
	"strconv"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

func (h Handler) principal(w http.ResponseWriter, r *http.Request, permission string) (authority.Principal, bool) {
	if h.Authority == nil {
		fail(w, authority.ErrUnavailable)
		return authority.Principal{}, false
	}
	p, e := h.Authority.AuthenticateRequest(r)
	if e != nil {
		fail(w, e)
		return p, false
	}
	p, e = h.Authority.AuthorizePrincipal(r.Context(), p, permission)
	if e != nil {
		fail(w, e)
		return p, false
	}
	return p, true
}
func boundedPage(r *http.Request, extra ...string) (int, int, error) {
	allowed := map[string]bool{"offset": true, "take": true}
	for _, k := range extra {
		allowed[k] = true
	}
	for k, v := range r.URL.Query() {
		if !allowed[k] || len(v) != 1 {
			return 0, 0, f.ErrInvalid
		}
	}
	offset, take := 0, 20
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

func (h Handler) fediverse(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/fediverse/moderation") {
		h.moderation(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/admin/fediverse/keys") {
		h.keyAdmin(w, r)
		return
	}
	repo, ok := h.Repository.(f.ClientRepository)
	if !ok {
		fail(w, f.ErrUnavailable)
		return
	}
	p, ok := h.principal(w, r, f.ReadPermission)
	if !ok {
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "fediverse" || parts[2] != "actors" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 4 && parts[3] == "availability" {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		v, e := repo.Availability(r.Context(), p.Account.ID)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
		return
	}
	if len(parts) == 4 && parts[3] == "search" {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		offset, take, e := boundedPage(r, "q")
		if e != nil || strings.TrimSpace(r.URL.Query().Get("q")) == "" {
			fail(w, f.ErrInvalid)
			return
		}
		v, e := repo.SearchRemoteProfiles(r.Context(), r.URL.Query().Get("q"), offset, take)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
		return
	}
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	var profile f.RemoteProfile
	var e error
	if authority.ValidID(parts[3]) {
		profile, e = repo.RemoteProfile(r.Context(), parts[3])
	} else {
		profile, e = repo.RemoteProfileByHandle(r.Context(), parts[3])
	}
	if e != nil {
		fail(w, e)
		return
	}
	if len(parts) == 4 {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		write(w, 200, profile, "application/json")
		return
	}
	if len(parts) != 5 {
		http.NotFound(w, r)
		return
	}
	switch parts[4] {
	case "posts":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		offset, take, e := boundedPage(r, "publisher_id")
		ref := r.URL.Query().Get("publisher_id")
		if e != nil || !authority.ValidID(ref) {
			fail(w, f.ErrInvalid)
			return
		}
		v, e := repo.RemotePosts(r.Context(), p.Account.ID, ref, parts[3], offset, take)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
	case "relationship":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		if len(r.URL.Query()) != 1 || !authority.ValidID(r.URL.Query().Get("publisher_id")) {
			fail(w, f.ErrInvalid)
			return
		}
		v, e := repo.RelationshipView(r.Context(), p.Account.ID, r.URL.Query().Get("publisher_id"), profile.Actor)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
	case "follow", "unfollow", "block", "unblock":
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		var input struct {
			PublisherID string `json:"publisher_id"`
		}
		if decode(w, r, &input) != nil || !authority.ValidID(input.PublisherID) {
			fail(w, f.ErrInvalid)
			return
		}
		if e = h.Repository.Change(r.Context(), p.Account.ID, input.PublisherID, f.Change{Action: parts[4], RemoteActor: profile.Actor}); e != nil {
			fail(w, e)
			return
		}
		write(w, 204, nil, "application/json")
	default:
		http.NotFound(w, r)
	}
}

func (h Handler) realm(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.Repository.(f.RealmRepository)
	if !ok {
		fail(w, f.ErrUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "activitypub" || parts[1] != "realms" || !f.ValidName(parts[2]) {
		http.NotFound(w, r)
		return
	}
	slug := parts[2]
	if len(parts) == 3 {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		v, e := repo.RealmActor(r.Context(), slug)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, f.MediaType)
		return
	}
	if parts[3] == "inbox" {
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
		v, e := f.Verify(r.Context(), h.Config, h.Keys, r, b)
		if e != nil {
			fail(w, e)
			return
		}
		if e = repo.ReceiveRealm(r.Context(), slug, v); e != nil {
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
	offset, take, e := page(r)
	if e != nil {
		fail(w, e)
		return
	}
	v, e := repo.RealmCollection(r.Context(), slug, parts[3], offset, take)
	if e != nil {
		fail(w, e)
		return
	}
	if r.URL.RawQuery == "" {
		v = f.Collection{Context: f.Context, ID: v.PartOf, Type: "OrderedCollection", Total: v.Total, First: v.PartOf + "?offset=0&take=20"}
	}
	write(w, 200, v, f.MediaType)
}

func (h Handler) moderation(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.Repository.(f.ModerationRepository)
	if !ok {
		fail(w, f.ErrUnavailable)
		return
	}
	p, ok := h.principal(w, r, f.ModerationPermission)
	if !ok {
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/fediverse/moderation")
	switch {
	case suffix == "/rules" && r.Method == "GET":
		offset, take, e := page(r)
		if e != nil {
			fail(w, e)
			return
		}
		v, e := repo.ModerationRules(r.Context(), p.Account.ID, offset, take)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
	case suffix == "/rules" && r.Method == "POST":
		if r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		var x f.ModerationRule
		if decode(w, r, &x) != nil || x.ID != "" {
			fail(w, f.ErrInvalid)
			return
		}
		v, e := repo.PutModerationRule(r.Context(), p.Account.ID, x)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 201, v, "application/json")
	case suffix == "/check" && r.Method == "POST":
		var input struct {
			Actor   string `json:"actor"`
			Content string `json:"content"`
		}
		if r.URL.RawQuery != "" || decode(w, r, &input) != nil {
			fail(w, f.ErrInvalid)
			return
		}
		v, e := repo.CheckModeration(r.Context(), p.Account.ID, input.Actor, input.Content)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
	case strings.HasPrefix(suffix, "/rules/"):
		id := strings.TrimPrefix(suffix, "/rules/")
		if !authority.ValidID(id) {
			http.NotFound(w, r)
			return
		}
		if r.Method == "GET" {
			if r.URL.RawQuery != "" {
				fail(w, f.ErrInvalid)
				return
			}
			v, e := repo.ModerationRule(r.Context(), p.Account.ID, id)
			if e != nil {
				fail(w, e)
				return
			}
			write(w, 200, v, "application/json")
			return
		}
		if r.Method == "DELETE" {
			if r.URL.RawQuery != "" {
				fail(w, f.ErrInvalid)
				return
			}
			if e := repo.DeleteModerationRule(r.Context(), p.Account.ID, id); e != nil {
				fail(w, e)
				return
			}
			write(w, 204, nil, "application/json")
			return
		}
		if r.Method == "PUT" {
			var x f.ModerationRule
			if r.URL.RawQuery != "" || decode(w, r, &x) != nil || x.ID != "" && x.ID != id {
				fail(w, f.ErrInvalid)
				return
			}
			x.ID = id
			v, e := repo.PutModerationRule(r.Context(), p.Account.ID, x)
			if e != nil {
				fail(w, e)
				return
			}
			write(w, 200, v, "application/json")
			return
		}
		method(w, "GET, PUT, DELETE")
	default:
		if suffix == "/rules" {
			method(w, "GET, POST")
		} else if suffix == "/check" {
			method(w, "POST")
		} else {
			http.NotFound(w, r)
		}
	}
}

func (h Handler) keyAdmin(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.Repository.(f.KeyAdminRepository)
	if !ok {
		fail(w, f.ErrUnavailable)
		return
	}
	p, ok := h.principal(w, r, f.KeyAdminPermission)
	if !ok {
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/admin/fediverse/keys")
	if r.URL.RawQuery != "" && suffix != "/audit" {
		fail(w, f.ErrInvalid)
		return
	}
	if suffix == "/stats" {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		v, e := repo.KeyStatistics(r.Context(), p.Account.ID)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
		return
	}
	if suffix == "/audit" {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		offset, take, e := page(r)
		if e != nil {
			fail(w, e)
			return
		}
		v, e := repo.KeyAudit(r.Context(), p.Account.ID, offset, take)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
		return
	}
	parts := strings.Split(strings.Trim(suffix, "/"), "/")
	if len(parts) < 2 || parts[0] != "actor" || !authority.ValidID(parts[1]) {
		http.NotFound(w, r)
		return
	}
	id := parts[1]
	if len(parts) == 2 {
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		v, e := repo.KeyInfo(r.Context(), p.Account.ID, id)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, v, "application/json")
		return
	}
	if len(parts) != 3 || r.Method != "POST" {
		method(w, "POST")
		return
	}
	var v f.KeyInfo
	var e error
	if parts[2] == "ensure" {
		v, e = repo.EnsureKey(r.Context(), p.Account.ID, id)
	} else if parts[2] == "rotate" {
		v, e = repo.RotateKey(r.Context(), p.Account.ID, id)
	} else {
		http.NotFound(w, r)
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v, "application/json")
}
