// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func RouteMatches(p string) bool {
	return p == "/api/livestreams" || strings.HasPrefix(p, "/api/livestreams/")
}
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	w.Header().Set("X-Solsynth-Projection", "owned-livestream-v1")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	switch {
	case errors.As(e, &f):
		respond(w, f.Status, f)
	case errors.As(e, &a):
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="livestreams"`)
		}
		respond(w, a.Status, a)
	default:
		respond(w, 503, ErrUnavailable)
	}
}
func method(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	respond(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
}
func validateJSON(d *json.Decoder, depth int) error {
	if depth > 24 {
		return ErrInvalid
	}
	token, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := key.(string)
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
func decode(w http.ResponseWriter, r *http.Request, out any, keys ...string) error {
	m, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || m != "application/json" {
		return &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if e != nil || !utf8.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if validateJSON(d, 0) != nil {
		return ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(b, &object) != nil {
		return ErrInvalid
	}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return ErrInvalid
		}
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrInvalid
	}
	return nil
}
func empty(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	var v map[string]any
	if e := decode(w, r, &v); e != nil {
		return e
	}
	if len(v) != 0 {
		return ErrInvalid
	}
	return nil
}
func (s *Service) principal(r *http.Request, required bool) (context.Context, string, error) {
	if s == nil || s.Authority == nil {
		return r.Context(), "", ErrUnavailable
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		if !required && len(r.Header.Values("Authorization")) == 0 && errors.Is(e, authority.ErrInvalid) {
			return r.Context(), "", nil
		}
		return r.Context(), "", e
	}
	p, e = s.Authority.AuthenticatePrincipal(r.Context(), p)
	if e != nil {
		return r.Context(), "", e
	}
	if !authority.ValidID(p.Account.ID) {
		return r.Context(), "", ErrUnauthorized
	}
	ctx := r.Context()
	if s.Bind != nil {
		ctx = s.Bind(ctx, p)
	}
	return ctx, p.Account.ID, nil
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if !RouteMatches(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.User != nil || r.URL.RawPath != "" {
		failure(w, ErrInvalid)
		return
	}
	for key := range r.Header {
		k := strings.ToLower(key)
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-app-secret" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, ErrForbidden)
			return
		}
	}
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/livestreams")
	parts := []string{}
	if rest != "" {
		parts = strings.Split(strings.TrimPrefix(rest, "/"), "/")
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	allowed := map[string]bool{}
	if len(parts) == 0 && r.Method == "POST" {
		allowed["pub"] = true
	}
	if r.Method == "GET" && (len(parts) == 0 || len(parts) == 2 && parts[0] == "publisher") {
		allowed["limit"] = true
		allowed["offset"] = true
	}
	if r.Method == "GET" && len(parts) == 2 && parts[1] == "token" {
		allowed["streamer"] = true
		allowed["tool"] = true
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			failure(w, ErrInvalid)
			return
		}
	}
	required := r.Method != "GET" || len(parts) == 2 && parts[1] == "token"
	ctx, actor, e := s.principal(r, required)
	if e != nil {
		failure(w, e)
		return
	}
	limit, offset := 50, 0
	for key, dst := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := q.Get(key); raw != "" {
			n, x := strconv.Atoi(raw)
			if x != nil {
				failure(w, ErrInvalid)
				return
			}
			*dst = n
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		failure(w, ErrInvalid)
		return
	}
	var out any
	status := 200
	switch {
	case len(parts) == 0:
		switch r.Method {
		case "GET":
			out, e = s.Repository.List(ctx, actor, "", limit, offset)
		case "POST":
			var req Patch
			e = decode(w, r, &req, "title", "description", "slug", "thumbnail_id", "type", "visibility", "metadata")
			if len(r.Header.Values("Idempotency-Key")) > 1 {
				e = ErrInvalid
			}
			if e == nil {
				out, e = s.Repository.Create(ctx, actor, q.Get("pub"), r.Header.Get("Idempotency-Key"), req)
			}
		default:
			method(w, "GET, POST")
			return
		}
	case len(parts) == 2 && parts[0] == "publisher":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		if !authority.ValidID(parts[1]) {
			failure(w, ErrInvalid)
			return
		}
		out, e = s.Repository.List(ctx, actor, parts[1], limit, offset)
	default:
		if len(parts) == 0 || !authority.ValidID(parts[0]) {
			failure(w, ErrNotFound)
			return
		}
		id := parts[0]
		if len(parts) > 1 && (parts[1] == "egress" || parts[1] == "hls" || parts[1] == "awards" || parts[1] == "chat") {
			failure(w, ErrUnsupported)
			return
		}
		switch {
		case len(parts) == 1:
			switch r.Method {
			case "GET":
				out, e = s.Repository.Get(ctx, actor, id)
			case "PATCH":
				var p Patch
				e = decode(w, r, &p, "title", "description", "slug", "type", "visibility", "metadata")
				if e == nil {
					out, e = s.Repository.Update(ctx, actor, id, p)
				}
			case "DELETE":
				e = empty(w, r)
				if e == nil {
					e = s.Repository.End(ctx, actor, id, true)
				}
			default:
				method(w, "GET, PATCH, DELETE")
				return
			}
		case len(parts) == 2 && parts[1] == "thumbnail":
			if r.Method != "PATCH" {
				method(w, "PATCH")
				return
			}
			var p struct {
				ID *string `json:"thumbnail_id"`
			}
			e = decode(w, r, &p, "thumbnail_id")
			if e == nil {
				out, e = s.Repository.Thumbnail(ctx, actor, id, p.ID)
			}
		case len(parts) == 2 && parts[1] == "start":
			if r.Method != "POST" {
				method(w, "POST")
				return
			}
			var req StartRequest
			e = decode(w, r, &req, "participant_name", "no_ingress", "use_whip", "enable_transcoding")
			if e == nil {
				out, e = s.Repository.Start(ctx, actor, id, req)
			}
		case len(parts) == 2 && parts[1] == "end":
			if r.Method != "POST" {
				method(w, "POST")
				return
			}
			e = empty(w, r)
			if e == nil {
				e = s.Repository.End(ctx, actor, id, false)
			}
		case len(parts) == 2 && parts[1] == "details":
			if r.Method != "GET" {
				method(w, "GET")
				return
			}
			out, e = s.Repository.Details(ctx, actor, id)
		case len(parts) == 2 && parts[1] == "token":
			if r.Method != "GET" {
				method(w, "GET")
				return
			}
			streamer, tool := false, false
			for key, dst := range map[string]*bool{"streamer": &streamer, "tool": &tool} {
				if vals, ok := q[key]; ok {
					if vals[0] != "true" && vals[0] != "false" {
						failure(w, ErrInvalid)
						return
					}
					*dst = vals[0] == "true"
				}
			}
			out, e = s.Repository.Token(ctx, actor, id, streamer, tool)
		default:
			http.NotFound(w, r)
			return
		}
	}
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, status, out)
}
