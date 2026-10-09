// SPDX-License-Identifier: AGPL-3.0-only
package collections

import (
	"errors"
	"net/http"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

func path(raw string) (string, bool) {
	for _, prefix := range []string{"/api/admin/collections", "/sphere/admin/collections"} {
		if raw == prefix {
			return "", true
		}
		if strings.HasPrefix(raw, prefix+"/") {
			id := strings.TrimPrefix(raw, prefix+"/")
			return id, authority.ValidID(id)
		}
	}
	return "", false
}
func Handles(raw string) bool { _, ok := path(raw); return ok }
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	if errors.As(e, &f) {
		admin.Respond(w, f.Status, f)
		return
	}
	admin.RespondError(w, e)
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := path(r.URL.EscapedPath())
		if !ok {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
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
		var out any
		total := -1
		switch {
		case id == "" && r.Method == "GET":
			q := Page{Query: r.URL.Query().Get("query"), PublisherID: r.URL.Query().Get("publisherId")}
			for key, p := range map[string]*int{"offset": &q.Offset, "take": &q.Take} {
				v, has := r.URL.Query()[key]
				if !has {
					continue
				}
				if len(v) != 1 {
					failure(w, ErrInvalid)
					return
				}
				n, x := strconv.Atoi(v[0])
				if x != nil || n < 0 {
					failure(w, ErrInvalid)
					return
				}
				*p = n
			}
			out, total, e = repo.List(r.Context(), actor, q)
		case id != "" && r.Method == "GET":
			out, e = repo.Get(r.Context(), actor, id)
		case id != "" && r.Method == "PATCH":
			var p Patch
			if admin.DecodeStrict(w, r, &p) != nil {
				e = ErrInvalid
				break
			}
			out, e = repo.Update(r.Context(), actor, id, p)
		case id != "" && r.Method == "DELETE":
			if admin.EmptyBody(w, r) != nil {
				e = ErrInvalid
				break
			}
			e = repo.Delete(r.Context(), actor, id)
			if e == nil {
				admin.Respond(w, 204, nil)
				return
			}
		default:
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			admin.Respond(w, 405, ErrInvalid)
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
