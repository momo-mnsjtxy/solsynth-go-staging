// SPDX-License-Identifier: AGPL-3.0-only
package realmboosts

import (
	"errors"
	"net/http"
	"net/url"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
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
	for i, v := range p {
		x, e := url.PathUnescape(v)
		if e != nil || x == "" || len(x) > 256 || strings.ContainsAny(x, "/\\\x00") {
			return nil
		}
		p[i] = x
	}
	return p
}
func Handles(path string) bool {
	p := parts(path)
	return len(p) == 3 && p[0] == "realms" && p[2] == "boosts" || len(p) == 4 && p[0] == "realms" && p[2] == "boosts" && p[3] == "leaderboard" || len(p) == 5 && p[0] == "realms" && p[2] == "members" && p[4] == "experience"
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	switch {
	case errors.As(e, &f):
		admin.Respond(w, f.Status, f)
	case errors.As(e, &a):
		admin.Respond(w, a.Status, a)
	case errors.Is(e, c.ErrIdempotency), errors.Is(e, c.ErrConflict):
		admin.Respond(w, 409, ErrConflict)
	default:
		admin.Respond(w, 503, ErrUnavailable)
	}
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Handles(r.URL.EscapedPath()) {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		p := parts(r.URL.EscapedPath())
		write := r.Method == "POST" && len(p) == 3
		if r.Method != "GET" && !write {
			allow := "GET"
			if len(p) == 3 {
				allow = "GET, POST"
			}
			w.Header().Set("Allow", allow)
			admin.Respond(w, 405, ErrInvalid)
			return
		}
		for k := range r.Header {
			k = strings.ToLower(k)
			if k == "x-account-id" || k == "x-user-id" || k == "x-role" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
				failure(w, authority.ErrForbidden)
				return
			}
		}
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
		if e != nil {
			failure(w, ErrInvalid)
			return
		}
		take := 20
		if len(p) == 5 {
			take = 50
		}
		for k, v := range q {
			if k != "take" || len(v) != 1 || len(p) == 3 {
				failure(w, ErrInvalid)
				return
			}
			take, e = strconv.Atoi(v[0])
			if e != nil || take < 1 || take > 200 {
				failure(w, ErrInvalid)
				return
			}
		}
		var out any
		switch {
		case write:
			if len(r.Header.Values("Idempotency-Key")) > 1 || len(r.Header.Get("Idempotency-Key")) > 128 {
				failure(w, ErrInvalid)
				return
			}
			var req Request
			if admin.DecodeStrict(w, r, &req) != nil {
				failure(w, ErrInvalid)
				return
			}
			out, e = repo.CreateOrder(r.Context(), actor, p[1], r.Header.Get("Idempotency-Key"), req)
		case len(p) == 3:
			out, e = repo.Status(r.Context(), actor, p[1])
		case len(p) == 4:
			out, e = repo.Leaderboard(r.Context(), actor, p[1], take)
		case len(p) == 5:
			w.Header().Set("X-Realm-Experience-Scope", "realm.boost.purchase")
			w.Header().Set("X-Realm-Experience-Complete", "false")
			out, e = repo.Experience(r.Context(), actor, p[1], p[3], take)
		}
		if e != nil {
			failure(w, e)
			return
		}
		admin.Respond(w, 200, out)
	})
}
