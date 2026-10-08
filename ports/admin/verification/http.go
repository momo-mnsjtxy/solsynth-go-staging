// SPDX-License-Identifier: AGPL-3.0-only
package verification

import (
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
)

func Route(path string) (Kind, string, bool) {
	p := strings.Split(strings.Trim(path, "/"), "/")
	if len(p) != 5 || p[0] != "api" || p[1] != "admin" || p[4] != "verification" || p[3] == "" || len(p[3]) > 256 {
		return "", "", false
	}
	var k Kind
	switch p[2] {
	case "accounts":
		k = Account
	case "publishers":
		k = Publisher
	case "realms":
		k = Realm
	default:
		return "", "", false
	}
	return k, p[3], true
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, ref, ok := Route(r.URL.Path)
		if !ok {
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
		if r.URL.RawQuery != "" {
			c.RespondError(w, c.ErrInvalid)
			return
		}
		var mark *accounts.VerificationMark
		switch r.Method {
		case "POST":
			e = c.DecodeStrict(w, r, &mark)
			if e == nil && mark == nil {
				e = c.ErrInvalid
			}
			if e == nil {
				e = accounts.ValidateVerification(mark)
			}
			if e != nil {
				c.RespondError(w, c.ErrInvalid)
				return
			}
		case "DELETE":
			if e = c.EmptyBody(w, r); e != nil {
				c.RespondError(w, e)
				return
			}
		default:
			c.Respond(w, 405, c.ErrInvalid)
			return
		}
		v, e := repo.SetVerification(r.Context(), actor, k, ref, mark)
		if e != nil {
			c.RespondError(w, e)
			return
		}
		status := 200
		if k == Account && r.Method == "DELETE" {
			status = 204
		}
		c.Respond(w, status, v)
	})
}
