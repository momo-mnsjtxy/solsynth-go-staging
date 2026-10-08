// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"unicode/utf8"
)

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	var c *accounts.Failure
	var p *posts.Failure
	var m *media.Failure
	status := 503
	switch {
	case errors.As(e, &f):
		status = f.Status
	case errors.As(e, &a):
		status = a.Status
	case errors.As(e, &c):
		status = c.Status
	case errors.As(e, &p):
		status = p.Status
	case errors.As(e, &m):
		status = m.Status
	default:
		e = ErrUnavailable
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="merchants"`)
	}
	respond(w, status, e)
}

// validateJSON rejects duplicate keys at every nesting level, including answer
// maps, instead of allowing a decoder's last-write-wins interpretation.
func validateJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return ErrInvalid
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return ErrInvalid
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if e = validateJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = validateJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return ErrInvalid
	}
	_, e = d.Token()
	return e
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	m, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || m != "application/json" {
		return &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if e != nil || !utf8.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = validateJSON(d, 0); e != nil {
		return ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(out); e != nil {
		return ErrInvalid
	}
	return nil
}
func empty(w http.ResponseWriter, r *http.Request) error {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if e != nil || len(bytes.TrimSpace(b)) > 0 {
		return ErrInvalid
	}
	return nil
}
