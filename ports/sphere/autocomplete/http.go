// SPDX-License-Identifier: AGPL-3.0-only
package autocomplete

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"unicode/utf8"
)

func Route(path string) (room string, ok bool) {
	if path == "/api/autocomplete" {
		return "", true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/chat/"), "/")
	if strings.HasPrefix(path, "/api/chat/") && len(parts) == 2 && parts[1] == "autocomplete" && authority.ValidID(parts[0]) {
		return parts[0], true
	}
	return "", false
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
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
		respond(w, 503, ErrUnavailable)
	}
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		room, ok := Route(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			respond(w, 405, &Failure{"METHOD_NOT_ALLOWED", "Method not allowed.", 405})
			return
		}
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
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
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(r.URL.RawQuery) > 128 {
			failure(w, ErrInvalid)
			return
		}
		for k, v := range q {
			if room != "" || k != "roomId" || len(v) != 1 || !authority.ValidID(v[0]) {
				failure(w, ErrInvalid)
				return
			}
		}
		if q.Has("roomId") {
			room = q.Get("roomId")
		}
		ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || ct != "application/json" {
			respond(w, 415, &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415})
			return
		}
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if e != nil || !utf8.Valid(b) {
			failure(w, ErrInvalid)
			return
		}
		d := json.NewDecoder(bytes.NewReader(b))
		token, e := d.Token()
		if e != nil || token != json.Delim('{') {
			failure(w, ErrInvalid)
			return
		}
		token, e = d.Token()
		if e != nil || token != "content" {
			failure(w, ErrInvalid)
			return
		}
		var content *string
		if e = d.Decode(&content); e != nil || content == nil {
			failure(w, ErrInvalid)
			return
		}
		token, e = d.Token()
		if e != nil || token != json.Delim('}') {
			failure(w, ErrInvalid)
			return
		}
		if _, e = d.Token(); e != io.EOF {
			failure(w, ErrInvalid)
			return
		}
		if _, e = Parse(*content); e != nil {
			failure(w, e)
			return
		}
		items, e := repo.Suggest(r.Context(), actor, room, *content)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, items)
	})
}
