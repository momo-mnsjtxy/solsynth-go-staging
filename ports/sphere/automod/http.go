// SPDX-License-Identifier: AGPL-3.0-only
package automod

import (
	"net/http"
	"net/url"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

func Handles(path string) bool {
	return path == "/api/automod/rules" || strings.HasPrefix(path, "/api/automod/rules/")
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
		tail := strings.TrimPrefix(r.URL.Path, "/api/automod/rules")
		var out any
		status := 200
		if tail == "" && r.Method == "GET" {
			q, queryErr := url.ParseQuery(r.URL.RawQuery)
			if queryErr != nil {
				c.RespondError(w, c.ErrInvalid)
				return
			}
			if vals, ok := q["enabledOnly"]; ok {
				if _, duplicate := q["enabled_only"]; duplicate {
					c.RespondError(w, c.ErrInvalid)
					return
				}
				q["enabled_only"] = vals
				delete(q, "enabledOnly")
			}
			if len(q) > 1 || len(q["enabled_only"]) > 1 || len(r.URL.RawQuery) > 64 {
				c.RespondError(w, c.ErrInvalid)
				return
			}
			var enabled *bool
			if raw, ok := q["enabled_only"]; ok {
				b, x := strconv.ParseBool(raw[0])
				if x != nil {
					c.RespondError(w, c.ErrInvalid)
					return
				}
				enabled = &b
			} else if r.URL.RawQuery != "" {
				c.RespondError(w, c.ErrInvalid)
				return
			}
			out, e = repo.Rules(r.Context(), actor, enabled)
		} else {
			if r.URL.RawQuery != "" {
				c.RespondError(w, c.ErrInvalid)
				return
			}
			parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
			switch {
			case tail == "" && r.Method == "POST":
				var p Patch
				e = c.DecodeStrict(w, r, &p)
				if e == nil {
					out, e = repo.CreateRule(r.Context(), actor, p)
					if e == nil {
						status = 201
						w.Header().Set("Location", "/api/automod/rules/"+out.(Rule).ID)
					}
				}
			case tail == "/bulk" && r.Method == "POST":
				var p []Patch
				e = c.DecodeStrict(w, r, &p)
				if e == nil {
					out, e = repo.BulkRules(r.Context(), actor, p)
				}
			case len(parts) == 1 && authority.ValidID(parts[0]):
				switch r.Method {
				case "GET":
					out, e = repo.Rule(r.Context(), actor, parts[0])
				case "PUT":
					var p Patch
					e = c.DecodeStrict(w, r, &p)
					if e == nil {
						out, e = repo.UpdateRule(r.Context(), actor, parts[0], p)
					}
				case "DELETE":
					e = c.EmptyBody(w, r)
					if e == nil {
						e = repo.DeleteRule(r.Context(), actor, parts[0])
						status = 204
					}
				default:
					c.Respond(w, 405, c.ErrInvalid)
					return
				}
			case len(parts) == 2 && authority.ValidID(parts[0]) && parts[1] == "test" && r.Method == "POST":
				var body struct {
					Content *string `json:"content"`
				}
				e = c.DecodeStrict(w, r, &body)
				text := ""
				if body.Content != nil {
					text = *body.Content
				}
				if e == nil {
					out, e = repo.TestRule(r.Context(), actor, parts[0], text)
				}
			default:
				http.NotFound(w, r)
				return
			}
		}
		if e != nil {
			c.RespondError(w, e)
			return
		}
		c.Respond(w, status, out)
	})
}
