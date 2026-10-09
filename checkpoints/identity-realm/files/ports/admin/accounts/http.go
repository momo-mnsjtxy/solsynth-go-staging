// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
)

// Handles is deliberately narrow: verification and board keep their existing
// owners; unsupported account-list/detail/provider routes are not swallowed.
func Handles(path string) bool {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(p) < 3 || p[0] != "api" || p[1] != "admin" || p[2] != "accounts" {
		return false
	}
	if extensionPath(p) {
		return true
	}
	if len(p) == 5 {
		return p[3] == "metrics" && p[4] == "activity" || p[4] == "contacts" || p[4] == "factors" || p[4] == "badges"
	}
	return p[4] == "badges" && (len(p) == 6 || len(p) == 7 && p[6] == "activate")
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Handles(r.URL.Path) {
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
		p := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if extensionPath(p) || len(p) == 5 && p[4] == "contacts" && r.Method == "POST" {
			serveExtension(w, r, repo, actor, p)
			return
		}
		if r.URL.RawQuery != "" {
			c.RespondError(w, c.ErrInvalid)
			return
		}
		if len(p[3]) > 256 || strings.TrimSpace(p[3]) != p[3] {
			c.RespondError(w, c.ErrInvalid)
			return
		}
		var out any
		status := 200
		if len(p) >= 6 && !authority.ValidID(p[5]) {
			c.RespondError(w, c.ErrInvalid)
			return
		}
		switch {
		case len(p) == 5 && r.Method == "GET":
			if e = c.EmptyBody(w, r); e != nil {
				break
			}
			switch p[4] {
			case "activity":
				out, e = repo.AccountActivityMetrics(r.Context(), actor)
			case "contacts":
				out, e = repo.AccountContacts(r.Context(), actor, p[3])
			case "factors":
				out, e = repo.AccountFactors(r.Context(), actor, p[3])
			case "badges":
				out, e = repo.AccountBadges(r.Context(), actor, p[3])
			}
		case len(p) == 5 && p[4] == "badges" && r.Method == "POST":
			var v *progression.AdminBadge
			if e = c.DecodeStrict(w, r, &v); e != nil {
				break
			}
			if v == nil {
				e = c.ErrInvalid
				break
			}
			if e = ValidateBadge(*v); e != nil {
				break
			}
			out, e = repo.GrantAccountBadge(r.Context(), actor, p[3], *v)
		case len(p) == 7 && r.Method == "POST":
			if e = c.EmptyBody(w, r); e == nil {
				e = repo.ActivateAccountBadge(r.Context(), actor, p[3], p[5])
			}
		case len(p) == 6 && r.Method == "DELETE":
			status = 204
			if e = c.EmptyBody(w, r); e == nil {
				e = repo.RevokeAccountBadge(r.Context(), actor, p[3], p[5])
			}
		default:
			c.Respond(w, 405, c.ErrInvalid)
			return
		}
		if e != nil {
			c.RespondError(w, e)
			return
		}
		if len(p) == 7 { // Authored activation returns an empty 200 response.
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Vary", "Authorization")
			w.WriteHeader(http.StatusOK)
			return
		}
		c.Respond(w, status, out)
	})
}
