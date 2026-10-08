// SPDX-License-Identifier: AGPL-3.0-only
package surveys

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strconv"
	"strings"
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
		w.Header().Set("WWW-Authenticate", `Bearer realm="surveys"`)
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
func page(r *http.Request, extras ...string) (Page, error) {
	p := Page{Take: 20}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return p, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 {
			return p, ErrInvalid
		}
		allowed := k == "take" || k == "offset"
		for _, x := range extras {
			allowed = allowed || x == k
		}
		if !allowed {
			return p, ErrUnsupported
		}
	}
	if q.Has("take") {
		p.Take, e = strconv.Atoi(q.Get("take"))
		if e != nil {
			return p, ErrInvalid
		}
	}
	if q.Has("offset") {
		p.Offset, e = strconv.Atoi(q.Get("offset"))
		if e != nil {
			return p, ErrInvalid
		}
	}
	if p.Take < 1 || p.Take > 100 || p.Offset < 0 || p.Offset > 100000 {
		return p, ErrInvalid
	}
	p.Publisher = q.Get("pub")
	if q.Has("active") {
		p.Active, e = strconv.ParseBool(q.Get("active"))
		if e != nil {
			return p, ErrInvalid
		}
	}
	return p, nil
}
func empty(w http.ResponseWriter, r *http.Request) error {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if e != nil || len(bytes.TrimSpace(b)) > 0 {
		return ErrInvalid
	}
	return nil
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		actor, e := auth.Authenticate(r)
		if e != nil {
			failure(w, e)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/surveys")
		if path == r.URL.Path || path != "" && !strings.HasPrefix(path, "/") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.Trim(path, "/"), "/")
		permission := ""
		if r.Method != "GET" {
			switch {
			case path == "" && r.Method == "POST":
				permission = "surveys.create"
			case len(parts) == 1 && r.Method == "PATCH":
				permission = "surveys.update"
			case len(parts) == 1 && r.Method == "DELETE":
				permission = "surveys.delete"
			case len(parts) == 2:
				permission = "surveys." + parts[1]
			}
			if actor == "" {
				failure(w, ErrUnauthenticated)
				return
			}
			if permission == "" {
				http.NotFound(w, r)
				return
			}
			if e = auth.Authorize(r, permission); e != nil {
				failure(w, e)
				return
			}
		}
		var out any
		status := 200
		switch {
		case path == "" && r.Method == "POST":
			var p Patch
			_, e = page(r, "pub")
			if e == nil {
				e = decode(w, r, &p)
			}
			if e == nil {
				out, e = repo.Create(r.Context(), actor, r.URL.Query().Get("pub"), p)
			}
		case path == "/me" && r.Method == "GET":
			var p Page
			p, e = page(r, "pub", "active")
			var n int64
			if e == nil {
				out, n, e = repo.List(r.Context(), actor, p)
				w.Header().Set("X-Total", strconv.FormatInt(n, 10))
			}
		case len(parts) == 1 && authority.ValidID(parts[0]):
			switch r.Method {
			case "GET":
				if r.URL.RawQuery != "" {
					e = ErrInvalid
				} else {
					out, e = repo.Get(r.Context(), actor, parts[0])
				}
			case "PATCH":
				var p Patch
				_, e = page(r, "pub")
				if e == nil {
					e = decode(w, r, &p)
				}
				if e == nil {
					out, e = repo.Update(r.Context(), actor, parts[0], p)
				}
			case "DELETE":
				e = empty(w, r)
				if e == nil {
					e = repo.Delete(r.Context(), actor, parts[0])
					status = 204
				}
			default:
				http.NotFound(w, r)
				return
			}
		case len(parts) == 2 && authority.ValidID(parts[0]):
			id, action := parts[0], parts[1]
			switch {
			case action == "answer" && r.Method == "POST":
				var req struct {
					Answer map[string]json.RawMessage `json:"answer"`
				}
				e = decode(w, r, &req)
				if e == nil {
					out, e = repo.Answer(r.Context(), actor, id, req.Answer)
				}
			case action == "answer" && r.Method == "DELETE":
				e = empty(w, r)
				if e == nil {
					e = repo.Withdraw(r.Context(), actor, id)
					status = 204
				}
			case (action == "publish" || action == "archive" || action == "clone") && r.Method == "POST":
				e = empty(w, r)
				if e == nil {
					out, e = repo.Transition(r.Context(), actor, id, action)
				}
			case action == "results" && r.Method == "GET":
				out, e = repo.Results(r.Context(), actor, id)
			case action == "feedback" && r.Method == "GET":
				var p Page
				p, e = page(r)
				var n int64
				if e == nil {
					out, n, e = repo.Feedback(r.Context(), actor, id, p)
					w.Header().Set("X-Total", strconv.FormatInt(n, 10))
				}
			default:
				http.NotFound(w, r)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, status, out)
	})
}
