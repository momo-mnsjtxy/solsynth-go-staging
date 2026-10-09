// SPDX-License-Identifier: AGPL-3.0-only
package translation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"unicode/utf8"
)

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
		if r.URL.Path != "/api/translate" {
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
		if e != nil || len(r.URL.RawQuery) > 256 {
			failure(w, ErrInvalid)
			return
		}
		for k, v := range q {
			if (k != "to" && k != "from") || len(v) != 1 {
				failure(w, ErrInvalid)
				return
			}
		}
		contentType, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || contentType != "application/json" {
			respond(w, 415, &Failure{"UNSUPPORTED_MEDIA_TYPE", "A JSON string is required.", 415})
			return
		}
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16000))
		if e != nil || !utf8.Valid(b) {
			failure(w, ErrInvalid)
			return
		}
		var text string
		d := json.NewDecoder(bytes.NewReader(b))
		if e = d.Decode(&text); e != nil {
			failure(w, ErrInvalid)
			return
		}
		if _, e = d.Token(); e != io.EOF {
			failure(w, ErrInvalid)
			return
		}
		req, e := (Request{Text: text, Target: q.Get("to"), Source: q.Get("from")}).Normalize()
		if e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Translate(r.Context(), actor, req)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
}
