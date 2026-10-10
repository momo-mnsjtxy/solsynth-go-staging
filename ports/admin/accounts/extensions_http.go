// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

func serveExtension(w http.ResponseWriter, r *http.Request, repo Repository, actor string, p []string) {
	ext, e := extensionRepository(repo)
	if e != nil {
		c.RespondError(w, e)
		return
	}
	var out any
	status := http.StatusOK
	if len(p) == 3 && r.Method == "GET" {
		q := r.URL.Query()
		f := Filter{Take: 50, Query: q.Get("query"), OrderBy: q.Get("orderBy")}
		for k, v := range q {
			if len(v) != 1 || (k != "take" && k != "offset" && k != "query" && k != "orderBy") {
				c.RespondError(w, c.ErrInvalid)
				return
			}
		}
		if q.Has("take") {
			f.Take, e = strconv.Atoi(q.Get("take"))
		}
		if e == nil && q.Has("offset") {
			f.Offset, e = strconv.Atoi(q.Get("offset"))
		}
		if e == nil {
			e = ValidateFilter(f)
		}
		if e == nil {
			e = c.EmptyBody(w, r)
		}
		var total int
		if e == nil {
			out, total, e = ext.ListAccounts(r.Context(), actor, f)
			w.Header().Set("X-Total", strconv.Itoa(total))
		}
	} else {
		if r.URL.RawQuery != "" || len(p) < 4 || len(p[3]) > 256 || strings.TrimSpace(p[3]) != p[3] {
			c.RespondError(w, c.ErrInvalid)
			return
		}
		switch {
		case len(p) == 4 && r.Method == "GET":
			e = c.EmptyBody(w, r)
			if e == nil {
				out, e = ext.AccountDetail(r.Context(), actor, p[3])
			}
		case len(p) == 5 && p[4] == "sessions" && r.Method == "GET":
			e = c.EmptyBody(w, r)
			if e == nil {
				out, e = ext.AccountSessions(r.Context(), actor, p[3])
			}
		default:
			m := Mutation{}
			if len(p) >= 6 {
				if !authority.ValidID(p[5]) {
					c.RespondError(w, c.ErrInvalid)
					return
				}
				m.ResourceID = p[5]
			}
			switch {
			case len(p) == 5 && p[4] == "credits" && r.Method == "POST":
				m.Operation = "credit.grant"
				e = c.DecodeStrict(w, r, &m)
				if e == nil && (m.Credit == nil || m.Contact != nil || m.Password != nil || m.Public != nil) {
					e = c.ErrInvalid
				}
			case len(p) == 5 && p[4] == "credentials" && r.Method == "PUT":
				m.Operation = "password.reset"
				e = c.DecodeStrict(w, r, &m)
				if e == nil && (m.Password == nil || m.Contact != nil || m.Public != nil || m.Credit != nil || len(*m.Password) < 12 || len(*m.Password) > 1024) {
					e = c.ErrInvalid
				}
			case len(p) == 5 && p[4] == "contacts" && r.Method == "POST":
				m.Operation = "contact.create"
				e = c.DecodeStrict(w, r, &m)
				if e == nil && (m.Contact == nil || m.Password != nil || m.Public != nil || m.Credit != nil) {
					e = c.ErrInvalid
				}
			case len(p) == 6 && p[4] == "contacts" && r.Method == "PATCH":
				m.Operation = "contact.visibility"
				e = c.DecodeStrict(w, r, &m)
				if e == nil && (m.Public == nil || m.Contact != nil || m.Password != nil || m.Credit != nil) {
					e = c.ErrInvalid
				}
			case len(p) == 7 && p[4] == "contacts" && r.Method == "POST":
				m.Operation = "contact." + p[6]
				e = c.EmptyBody(w, r)
			case len(p) == 6 && r.Method == "DELETE":
				status = 204
				e = c.EmptyBody(w, r)
				switch p[4] {
				case "contacts":
					m.Operation = "contact.delete"
				case "factors":
					m.Operation = "factor.delete"
				case "sessions":
					m.Operation = "session.revoke"
				default:
					e = c.ErrInvalid
				}
			default:
				c.Respond(w, 405, c.ErrInvalid)
				return
			}
			if e == nil {
				out, e = ext.MutateAccount(r.Context(), actor, p[3], m)
			}
		}
	}
	if e != nil {
		c.RespondError(w, e)
		return
	}
	c.Respond(w, status, out)
}
