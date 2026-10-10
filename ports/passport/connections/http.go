// SPDX-License-Identifier: AGPL-3.0-only
package connections

import (
	"context"
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func Handles(p string) bool { return strings.HasPrefix(p, "/api/accounts/me/connections/") }
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/accounts/me/connections/"), "/")
		if !Handles(r.URL.Path) || len(p) > 2 || p[0] == "" {
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" || r.URL.User != nil {
			c.RespondError(w, ErrInvalid)
			return
		}
		if s.Repository == nil || s.Authority == nil || s.Bind == nil {
			c.RespondError(w, ErrUnavailable)
			return
		}
		proof, e := s.Authority.AuthenticateRequest(r)
		if e != nil {
			c.RespondError(w, e)
			return
		}
		ctx := s.Bind(r.Context(), proof)
		actor := proof.Account.ID
		var out any
		status := 200
		switch {
		case len(p) == 2 && ValidProvider(p[0]) && p[1] == "start" && r.Method == "POST":
			e = c.EmptyBody(w, r)
			if e == nil {
				out, e = s.Repository.Start(ctx, actor, p[0])
			}
		case len(p) == 2 && ValidProvider(p[0]) && p[1] == "complete" && r.Method == "POST":
			var v Completion
			e = c.DecodeStrict(w, r, &v)
			if e == nil {
				out, e = s.Repository.Complete(ctx, actor, p[0], v)
			}
		case len(p) == 1 && authority.ValidID(p[0]) && r.Method == "DELETE":
			status = 204
			e = c.EmptyBody(w, r)
			if e == nil {
				e = s.Repository.Delete(ctx, actor, p[0])
			}
		case len(p) == 1 && authority.ValidID(p[0]) && r.Method == "PATCH":
			var v struct {
				Public *bool `json:"is_public"`
			}
			e = c.DecodeStrict(w, r, &v)
			if e == nil && v.Public == nil {
				e = ErrInvalid
			}
			if e == nil {
				out, e = s.Repository.SetPublic(ctx, actor, p[0], *v.Public)
			}
		default:
			c.Respond(w, 405, ErrInvalid)
			return
		}
		if e != nil {
			c.RespondError(w, e)
			return
		}
		c.Respond(w, status, out)
	})
}
