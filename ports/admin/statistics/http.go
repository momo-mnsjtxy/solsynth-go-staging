// SPDX-License-Identifier: AGPL-3.0-only
package statistics

import (
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"strings"
)

// QualifiedRoute is the deterministic combined-host alias. Each single-service
// handler also preserves its own /api/admin/stats contract through configuration.
func QualifiedRoute(path string) (Service, bool) {
	p := strings.Split(strings.Trim(path, "/"), "/")
	if len(p) == 4 && p[0] == "api" && p[1] == "admin" && p[3] == "stats" {
		s := Service(p[2])
		return s, s.Permission() != ""
	}
	return "", false
}
func NewHandler(repo Repository, auth Auth, service Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := service
		if qualified, ok := QualifiedRoute(r.URL.Path); ok {
			if s != "" && s != qualified {
				http.NotFound(w, r)
				return
			}
			s = qualified
		} else if r.URL.Path != "/api/admin/stats" || s.Permission() == "" {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
			c.RespondError(w, c.ErrUnavailable)
			return
		}
		actor, e := auth.Authenticate(r)
		if e != nil {
			c.RespondError(w, e)
			return
		}
		if r.Method != "GET" {
			c.Respond(w, 405, c.ErrInvalid)
			return
		}
		if r.URL.RawQuery != "" {
			c.RespondError(w, c.ErrInvalid)
			return
		}
		v, e := repo.Statistics(r.Context(), actor, s)
		if e != nil {
			c.RespondError(w, e)
			return
		}
		c.Respond(w, 200, v)
	})
}
