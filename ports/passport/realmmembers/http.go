// SPDX-License-Identifier: AGPL-3.0-only
package realmmembers

import (
	"errors"
	"net/http"
	"net/url"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	"solsynth.local/go-migration/ports/passport/social"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strconv"
	"strings"
)

func parts(path string) []string {
	if strings.HasPrefix(path, "/api/") {
		path = strings.TrimPrefix(path, "/api/")
	} else if strings.HasPrefix(path, "/passport/") {
		path = strings.TrimPrefix(path, "/passport/")
	} else {
		return nil
	}
	p := strings.Split(path, "/")
	for i, s := range p {
		x, e := url.PathUnescape(s)
		if e != nil || x == "" || x == "." || x == ".." || len(x) > 256 || strings.ContainsAny(x, "/\\\x00") {
			return nil
		}
		p[i] = x
	}
	return p
}
func Handles(path string) bool {
	p := parts(path)
	return len(p) >= 3 && p[0] == "realms" && (p[2] == "labels" && (len(p) == 3 || len(p) == 4) || len(p) == 4 && p[2] == "progression" && p[3] == "tenure" || p[2] == "members" && (len(p) == 5 && (p[4] == "label" || p[4] == "progression" || p[4] == "experience") || len(p) == 6 && p[4] == "progression" && p[5] == "history"))
}

// ExperienceRoute is the authored array-shaped known-source ledger route.
func ExperienceRoute(path string) bool {
	p := parts(path)
	return len(p) == 5 && p[0] == "realms" && p[2] == "members" && p[4] == "experience"
}

// Only explicit label filtering is intercepted; the established member listing
// remains owned by social and receives the same optional enrichment provider.
func HandlesRequest(r *http.Request) bool {
	if Handles(r.URL.EscapedPath()) {
		return true
	}
	p := parts(r.URL.EscapedPath())
	if len(p) != 3 || p[0] != "realms" || p[2] != "members" {
		return false
	}
	for _, pair := range strings.Split(r.URL.RawQuery, "&") {
		k, _, _ := strings.Cut(pair, "=")
		key, e := url.QueryUnescape(k)
		if e == nil && key == "label_id" {
			return true
		}
	}
	return false
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	var s *social.Failure
	var boost *b.Failure
	switch {
	case errors.As(e, &f):
		admin.Respond(w, f.Status, f)
	case errors.As(e, &a):
		admin.Respond(w, a.Status, a)
	case errors.As(e, &s):
		admin.Respond(w, s.Status, s)
	case errors.As(e, &boost):
		admin.Respond(w, boost.Status, boost)
	case errors.Is(e, c.ErrConflict), errors.Is(e, c.ErrIdempotency):
		admin.Respond(w, 409, ErrConflict)
	case errors.Is(e, c.ErrRetry):
		admin.Respond(w, 409, ErrRetry)
	case errors.Is(e, c.ErrForbidden):
		admin.Respond(w, 403, ErrForbidden)
	default:
		admin.Respond(w, 503, ErrUnavailable)
	}
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !HandlesRequest(r) {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		p := parts(r.URL.EscapedPath())
		allowed := "GET"
		if p[2] == "labels" {
			if len(p) == 3 {
				allowed = "GET, POST"
			} else {
				allowed = "PATCH, DELETE"
			}
		} else if p[2] == "progression" {
			allowed = "POST"
		} else if len(p) == 5 && p[4] == "label" {
			allowed = "PATCH"
		}
		valid := false
		for _, v := range strings.Split(allowed, ", ") {
			valid = valid || r.Method == v
		}
		if !valid {
			w.Header().Set("Allow", allowed)
			admin.Respond(w, 405, ErrInvalid)
			return
		}
		for k := range r.Header {
			k = strings.ToLower(k)
			if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-realm-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
				failure(w, authority.ErrForbidden)
				return
			}
		}
		write := r.Method != "GET"
		actor, e := auth(r, write)
		if e != nil {
			failure(w, e)
			return
		}
		if write && actor == "" {
			failure(w, authority.ErrInvalid)
			return
		}
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(r.URL.RawQuery) > 4096 {
			failure(w, ErrInvalid)
			return
		}
		take, offset := 50, 0
		if len(p) == 3 && p[2] == "members" {
			take = 20
		}
		labelID := ""
		for k, v := range q {
			if len(v) != 1 || write {
				failure(w, ErrInvalid)
				return
			}
			switch {
			case k == "take" && (len(p) == 6 || ExperienceRoute(r.URL.EscapedPath()) || len(p) == 3 && p[2] == "members"):
				take, e = strconv.Atoi(v[0])
				if e != nil || take < 1 || take > 200 {
					failure(w, ErrInvalid)
					return
				}
			case k == "offset" && len(p) == 3 && p[2] == "members":
				offset, e = strconv.Atoi(v[0])
				if e != nil || offset < 0 || offset > 1000000 {
					failure(w, ErrInvalid)
					return
				}
			case k == "label_id" && len(p) == 3 && p[2] == "members":
				labelID = v[0]
				if !authority.ValidID(labelID) {
					failure(w, ErrInvalid)
					return
				}
			default:
				failure(w, ErrInvalid)
				return
			}
		}
		var out any
		status := 200
		switch {
		case p[2] == "labels" && len(p) == 3 && r.Method == "GET":
			out, e = repo.Labels(r.Context(), actor, p[1])
		case p[2] == "labels" && (r.Method == "POST" || r.Method == "PATCH"):
			var body LabelRequest
			if admin.DecodeStrict(w, r, &body) != nil {
				failure(w, ErrInvalid)
				return
			}
			if r.Method == "POST" {
				if len(r.Header.Values("Idempotency-Key")) > 1 || len(r.Header.Get("Idempotency-Key")) > 128 {
					failure(w, ErrInvalid)
					return
				}
				out, e = repo.CreateLabel(r.Context(), actor, p[1], r.Header.Get("Idempotency-Key"), body)
			} else {
				out, e = repo.UpdateLabel(r.Context(), actor, p[1], p[3], body)
			}
		case p[2] == "labels" && r.Method == "DELETE":
			e = repo.DeleteLabel(r.Context(), actor, p[1], p[3])
			status = 204
		case len(p) == 3 && p[2] == "members":
			var total int64
			out, total, e = repo.MembersWithLabel(r.Context(), actor, p[1], labelID, offset, take)
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		case len(p) == 5 && p[4] == "label":
			var body AssignmentRequest
			if admin.DecodeStrict(w, r, &body) != nil {
				failure(w, ErrInvalid)
				return
			}
			out, e = repo.AssignLabel(r.Context(), actor, p[1], p[3], body.LabelID)
		case len(p) == 5 && p[4] == "progression":
			out, e = repo.Progression(r.Context(), actor, p[1], p[3])
		case len(p) == 6 || ExperienceRoute(r.URL.EscapedPath()):
			out, e = repo.History(r.Context(), actor, p[1], p[3], take)
			w.Header().Set("X-Realm-Experience-Scope", "realm.boost.purchase,realm.tenure.daily")
			w.Header().Set("X-Realm-Experience-Complete", "false")
		case p[2] == "progression":
			var body TenureRequest
			if admin.DecodeStrict(w, r, &body) != nil {
				failure(w, ErrInvalid)
				return
			}
			out, e = repo.GrantTenure(r.Context(), actor, p[1], body)
		}
		if e != nil {
			failure(w, e)
			return
		}
		if status == 204 {
			w.WriteHeader(status)
			return
		}
		admin.Respond(w, status, out)
	})
}
