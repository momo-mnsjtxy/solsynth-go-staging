// SPDX-License-Identifier: AGPL-3.0-only
package reader

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
)

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	switch {
	case errors.As(e, &f):
		respond(w, f.Status, f)
	case errors.As(e, &a):
		respond(w, a.Status, a)
	default:
		respond(w, ErrUnavailable.Status, ErrUnavailable)
	}
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		method := "GET"
		if path == "/api/scrap/link/cache" || path == "/api/scrap/cache/all" {
			method = "DELETE"
		} else if path != "/api/scrap/link" {
			http.NotFound(w, r)
			return
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			respond(w, 405, &Failure{"METHOD_NOT_ALLOWED", "Method not allowed.", 405})
			return
		}
		if repo == nil {
			failure(w, ErrUnavailable)
			return
		}
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(r.URL.RawQuery) > MaxURLBytes*3+16 {
			failure(w, ErrInvalid)
			return
		}
		for k, v := range q {
			if k != "url" || len(v) != 1 || path == "/api/scrap/cache/all" {
				failure(w, ErrInvalid)
				return
			}
		}
		raw := q.Get("url")
		if path != "/api/scrap/cache/all" && raw == "" {
			failure(w, ErrRequired)
			return
		}
		if r.Body != nil {
			b, e := io.ReadAll(io.LimitReader(r.Body, 2))
			if e != nil || len(b) > 0 {
				failure(w, ErrInvalid)
				return
			}
		}
		if method == "GET" {
			v, e := repo.Preview(r.Context(), raw)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
			return
		}
		if auth == nil {
			failure(w, ErrUnauthenticated)
			return
		}
		actor, e := auth.Authenticate(r)
		if e != nil {
			failure(w, e)
			return
		}
		if actor == "" {
			failure(w, ErrUnauthenticated)
			return
		}
		if e = auth.Authorize(r, "cache.scrap"); e != nil {
			failure(w, e)
			return
		}
		message := "Cache invalidated for URL"
		if path == "/api/scrap/cache/all" {
			e = repo.InvalidateAll(r.Context(), actor)
			message = "All link preview caches invalidated"
		} else {
			e = repo.Invalidate(r.Context(), actor, raw)
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]string{"message": message})
	})
}
