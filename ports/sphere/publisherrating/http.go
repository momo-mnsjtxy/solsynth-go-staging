// SPDX-License-Identifier: AGPL-3.0-only
package publisherrating

import (
	"net/http"
	admin "solsynth.local/go-migration/ports/admin/content"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	"strconv"
)

func Handles(path string) bool {
	p := s.Parts(path)
	return len(p) == 2 && p[0] == "publishers" && p[1] == "leaderboard" || (len(p) == 3 || len(p) == 4) && p[0] == "publishers" && p[2] == "rating"
}
func NewHandler(repo Repository, auth s.Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Handles(r.URL.EscapedPath()) {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
			s.WriteError(w, s.ErrUnavailable)
			return
		}
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			admin.Respond(w, 405, s.ErrInvalid)
			return
		}
		actor, e := auth(r, false)
		if e != nil {
			s.WriteError(w, e)
			return
		}
		q, e := s.QueryPage(r)
		if e != nil {
			s.WriteError(w, e)
			return
		}
		p := s.Parts(r.URL.EscapedPath())
		var out any
		total := -1
		switch {
		case len(p) == 2:
			out, total, e = repo.Leaderboard(r.Context(), actor, q)
		case len(p) == 3:
			out, e = repo.Rating(r.Context(), actor, p[1])
		case p[3] == "overview":
			out, e = repo.Overview(r.Context(), actor, p[1])
		case p[3] == "history":
			out, total, e = repo.History(r.Context(), actor, p[1], q)
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			s.WriteError(w, e)
			return
		}
		w.Header().Set("X-Publisher-Rating-Policy", Policy)
		if total >= 0 {
			w.Header().Set("X-Total", strconv.Itoa(total))
		}
		admin.Respond(w, 200, out)
	})
}
