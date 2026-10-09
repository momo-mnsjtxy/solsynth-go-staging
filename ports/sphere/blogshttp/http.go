// SPDX-License-Identifier: AGPL-3.0-only
package blogshttp

import (
	"errors"
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"strings"
)

func Handles(path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return len(p) >= 4 && p[0] == "api" && p[1] == "publishers" && p[3] == "domains"
}
func FailureResponse(w http.ResponseWriter, e error) {
	var f *blogs.Failure
	var a *accounts.Failure
	if errors.As(e, &f) {
		c.Respond(w, f.Status, f)
		return
	}
	if errors.As(e, &a) {
		c.Respond(w, a.Status, a)
		return
	}
	c.RespondError(w, e)
}
func NewHandler(repo blogs.Repository, auth blogs.Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Handles(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
			FailureResponse(w, blogs.ErrUnavailable)
			return
		}
		actor, e := auth.Authenticate(r)
		if e != nil {
			FailureResponse(w, e)
			return
		}
		if !authority.ValidID(actor) {
			FailureResponse(w, authority.ErrInvalid)
			return
		}
		if r.URL.RawQuery != "" {
			FailureResponse(w, blogs.ErrInvalid)
			return
		}
		p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if p[2] == "" || len(p[2]) > 256 {
			FailureResponse(w, blogs.ErrInvalid)
			return
		}
		var out any
		status := 200
		switch {
		case len(p) == 4 && r.Method == "GET":
			out, e = repo.List(r.Context(), actor, p[2])
		case len(p) == 4 && r.Method == "POST":
			var v struct {
				Domain string `json:"domain"`
			}
			if e = c.DecodeStrict(w, r, &v); e == nil {
				out, e = repo.Add(r.Context(), actor, p[2], v.Domain)
			}
		case len(p) == 5 && r.Method == "DELETE" && authority.ValidID(p[4]):
			if e = c.EmptyBody(w, r); e == nil {
				e = repo.Remove(r.Context(), actor, p[2], p[4])
			}
			status = 204
		case len(p) == 6 && p[5] == "recheck" && r.Method == "POST" && authority.ValidID(p[4]):
			if e = c.EmptyBody(w, r); e == nil {
				out, e = repo.Recheck(r.Context(), actor, p[2], p[4])
			}
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			FailureResponse(w, e)
			return
		}
		c.Respond(w, status, out)
	})
}
