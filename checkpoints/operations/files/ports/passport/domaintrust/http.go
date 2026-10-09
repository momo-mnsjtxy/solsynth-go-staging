// SPDX-License-Identifier: AGPL-3.0-only
package domaintrust

import (
	"errors"
	"net/http"
	"net/url"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

func route(path string) ([]string, bool) {
	for _, prefix := range []string{"/api/domain-blocks", "/passport/domain-blocks"} {
		if path == prefix {
			return nil, true
		}
		if strings.HasPrefix(path, prefix+"/") {
			parts := strings.Split(strings.TrimPrefix(path, prefix+"/"), "/")
			for i, v := range parts {
				out, e := url.PathUnescape(v)
				if e != nil || out == "" || strings.ContainsAny(out, "/\\\x00") {
					return nil, false
				}
				parts[i] = out
			}
			return parts, true
		}
	}
	return nil, false
}
func Handles(path string) bool { _, ok := route(path); return ok }
func failure(w http.ResponseWriter, e error) {
	var d *Failure
	var a *authority.Failure
	var c *admin.Failure
	switch {
	case errors.As(e, &d):
		admin.Respond(w, d.Status, d)
	case errors.As(e, &a):
		admin.Respond(w, a.Status, a)
	case errors.As(e, &c):
		admin.Respond(w, c.Status, c)
	default:
		admin.Respond(w, 503, ErrUnavailable)
	}
}
func page(r *http.Request) (Page, error) {
	out := Page{}
	for key, p := range map[string]*int{"offset": &out.Offset, "limit": &out.Limit} {
		v, ok := r.URL.Query()[key]
		if !ok {
			continue
		}
		if len(v) != 1 {
			return out, ErrInvalid
		}
		n, e := strconv.Atoi(v[0])
		if e != nil || n < 0 {
			return out, ErrInvalid
		}
		*p = n
	}
	return out.Bounds(), nil
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := route(r.URL.EscapedPath())
		if !ok {
			http.NotFound(w, r)
			return
		}
		if repo == nil {
			failure(w, ErrUnavailable)
			return
		}
		if len(p) == 1 && (p[0] == "check" || p[0] == "validate") {
			var raw string
			if p[0] == "check" && r.Method == "GET" {
				v := r.URL.Query()["url"]
				if len(v) != 1 {
					failure(w, ErrInvalid)
					return
				}
				raw = v[0]
			} else if p[0] == "validate" && r.Method == "POST" {
				var body struct {
					URL string `json:"url"`
				}
				if admin.DecodeStrict(w, r, &body) != nil {
					failure(w, ErrInvalid)
					return
				}
				raw = body.URL
			} else {
				admin.Respond(w, 405, ErrInvalid)
				return
			}
			if raw == "" || len(raw) > 8192 {
				failure(w, ErrInvalid)
				return
			}
			out, e := repo.Validate(r.Context(), raw)
			if e != nil {
				failure(w, e)
				return
			}
			out.MatchedRule = nil
			if p[0] == "check" {
				admin.Respond(w, 200, struct {
					IsAllowed   bool    `json:"is_allowed"`
					IsVerified  bool    `json:"is_verified"`
					BlockReason *string `json:"block_reason"`
				}{out.IsAllowed, out.IsVerified, out.BlockReason})
			} else {
				admin.Respond(w, 200, out)
			}
			return
		}
		if auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		actor, e := auth(r, true)
		if e != nil {
			failure(w, e)
			return
		}
		if actor == "" {
			failure(w, authority.ErrInvalid)
			return
		}
		q, e := page(r)
		if e != nil {
			failure(w, e)
			return
		}
		var out any
		total := -1
		switch {
		case len(p) == 0 && r.Method == "GET":
			out, total, e = repo.List(r.Context(), actor, q)
		case len(p) == 0 && r.Method == "POST":
			var body Create
			if admin.DecodeStrict(w, r, &body) != nil {
				e = ErrInvalid
				break
			}
			out, e = repo.Create(r.Context(), actor, body)
		case len(p) == 1 && p[0] == "metrics" && r.Method == "GET":
			out, total, e = repo.Metrics(r.Context(), actor, q)
		case len(p) == 2 && p[0] == "metrics" && r.Method == "GET":
			out, e = repo.Metric(r.Context(), actor, p[1])
		case len(p) == 2 && p[0] == "metrics" && p[1] == "purge" && r.Method == "POST":
			var body MetricMaintenance
			if admin.DecodeStrict(w, r, &body) != nil {
				e = ErrInvalid
				break
			}
			out, e = repo.PurgeMetrics(r.Context(), actor, body)
		case len(p) == 1 && authority.ValidID(p[0]) && r.Method == "GET":
			out, e = repo.Get(r.Context(), actor, p[0])
		case len(p) == 1 && authority.ValidID(p[0]) && r.Method == "PATCH":
			var body Patch
			if admin.DecodeStrict(w, r, &body) != nil {
				e = ErrInvalid
				break
			}
			out, e = repo.Update(r.Context(), actor, p[0], body)
		case len(p) == 1 && authority.ValidID(p[0]) && r.Method == "DELETE":
			if admin.EmptyBody(w, r) != nil {
				e = ErrInvalid
				break
			}
			e = repo.Delete(r.Context(), actor, p[0])
			if e == nil {
				admin.Respond(w, 204, nil)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			failure(w, e)
			return
		}
		if total >= 0 {
			w.Header().Set("X-Total", strconv.Itoa(total))
		}
		admin.Respond(w, 200, out)
	})
}
