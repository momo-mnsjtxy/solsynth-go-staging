// SPDX-License-Identifier: AGPL-3.0-only
package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func Handles(path string) bool {
	return path == "/api/activities" || strings.HasPrefix(path, "/api/activities/")
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) {
		send(w, f.Status, f)
	} else if errors.As(e, &a) {
		send(w, a.Status, a)
	} else {
		send(w, 503, ErrUnavailable)
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	var raw json.RawMessage
	if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrInvalid
	}
	if request, ok := v.(*Request); ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return ErrInvalid
		}
		for key, value := range fields {
			if strings.EqualFold(key, "visibility") {
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return ErrInvalid
				}
				request.VisibilitySpecified = true
			}
		}
	}
	return nil
}
func parseFilter(q url.Values, take int) (f Filter, e error) {
	f.Take = take
	allowed := map[string]bool{"includeExpired": true, "offset": true, "take": true, "query": true, "type": true, "provider": true, "referenceId": true, "term": true, "tags": true, "visibility": true, "catalogId": true, "from": true, "to": true, "isActive": true}
	for k, vs := range q {
		if !allowed[k] || k != "tags" && len(vs) != 1 {
			return f, ErrInvalid
		}
	}
	for k, p := range map[string]*int{"offset": &f.Offset, "take": &f.Take} {
		if v := q.Get(k); v != "" {
			*p, e = strconv.Atoi(v)
			if e != nil {
				return f, ErrInvalid
			}
		}
	}
	if f.Offset < 0 || f.Offset > 100000 || f.Take < 1 || f.Take > 200 {
		return f, ErrInvalid
	}
	if v := q.Get("includeExpired"); v != "" {
		f.IncludeExpired, e = strconv.ParseBool(v)
		if e != nil {
			return f, ErrInvalid
		}
	}
	if v := q.Get("isActive"); v != "" {
		x, e := strconv.ParseBool(v)
		if e != nil {
			return f, ErrInvalid
		}
		f.IsActive = &x
	}
	if v := q.Get("visibility"); v != "" {
		x, e := strconv.Atoi(v)
		if e != nil || x < Private || x > Friends {
			return f, ErrInvalid
		}
		f.Visibility = &x
		f.Filtered = true
	}
	for k, p := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(k); v != "" {
			x, e := time.Parse(time.RFC3339Nano, v)
			if e != nil {
				return f, ErrInvalid
			}
			*p = &x
			f.Filtered = true
		}
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return f, ErrInvalid
	}
	for k, p := range map[string]*string{"query": &f.Query, "type": &f.Type, "provider": &f.Provider, "referenceId": &f.ReferenceID, "term": &f.Term, "catalogId": &f.CatalogID} {
		*p = strings.TrimSpace(q.Get(k))
		if len(*p) > 4096 || strings.ContainsRune(*p, 0) {
			return f, ErrInvalid
		}
		if *p != "" {
			f.Filtered = true
		}
	}
	if len(q["tags"]) > 64 {
		return f, ErrInvalid
	}
	f.Tags = []string{}
	seen := map[string]bool{}
	for _, s := range q["tags"] {
		s = strings.TrimSpace(s)
		if len(s) > 256 || strings.ContainsRune(s, 0) {
			return f, ErrInvalid
		}
		if s != "" && !seen[s] {
			seen[s] = true
			f.Tags = append(f.Tags, s)
			f.Filtered = true
		}
	}
	return f, nil
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	if !Handles(r.URL.Path) {
		w.WriteHeader(404)
		return
	}
	path := r.URL.Path
	tail := strings.TrimPrefix(path, "/api/activities/")
	public := r.Method == "GET" && path != "/api/activities" && tail != "catalog" && tail != "stats" && !strings.Contains(tail, "/")
	authPresent := false
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			authPresent = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, authority.ErrForbidden)
			return
		}
	}
	for k := range r.URL.Query() {
		if k == "token" || k == "access_token" || k == "tk" {
			failure(w, ErrInvalid)
			return
		}
	}
	if r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	ctx := r.Context()
	actor := ""
	if !public || authPresent {
		if s.Authority == nil || s.Bind == nil {
			failure(w, authority.ErrUnavailable)
			return
		}
		p, e := s.Authority.AuthenticateRequest(r)
		if e != nil {
			failure(w, e)
			return
		}
		actor = p.Account.ID
		ctx = s.Bind(ctx, p)
	}
	if r.Method == "GET" {
		f, e := parseFilter(r.URL.Query(), 20)
		if e != nil {
			failure(w, e)
			return
		}
		switch path {
		case "/api/activities/catalog":
			f, e = parseFilter(r.URL.Query(), 50)
			if e != nil {
				failure(w, e)
				return
			}
			v, e := s.Repository.Catalog(ctx, actor, f)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		case "/api/activities/stats", "/api/activities/tags/stats":
			v, e := s.Repository.Stats(ctx, actor, f, path == "/api/activities/tags/stats")
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		default:
			target := actor
			if path != "/api/activities" {
				if !public {
					w.WriteHeader(404)
					return
				}
				target = tail
				if target == "me" {
					if actor == "" {
						failure(w, authority.ErrInvalid)
						return
					}
					target = actor
				}
				f = Filter{Take: 20}
			}
			v, n, e := s.Repository.List(ctx, actor, target, f)
			if e != nil {
				failure(w, e)
				return
			}
			if f.IncludeExpired || f.Filtered {
				w.Header().Set("X-Total", strconv.Itoa(n))
			}
			send(w, 200, v)
		}
		return
	}
	if r.Method == "POST" && path == "/api/activities/sessions/end" {
		var q struct {
			ManualID string `json:"manual_id"`
		}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		v, e := s.Repository.End(ctx, actor, q.ManualID)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, v)
		return
	}
	if r.Method == "POST" && (path == "/api/activities" || path == "/api/activities/sessions/start") || r.Method == "PUT" && authority.ValidID(tail) {
		q := Request{LeaseMinutes: 5, Visibility: Public}
		if e := decode(w, r, &q); e != nil {
			failure(w, e)
			return
		}
		id := ""
		if r.Method == "PUT" {
			id = tail
		}
		v, e := s.Repository.Set(ctx, actor, q, path == "/api/activities/sessions/start", id)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, v)
		return
	}
	if r.Method == "DELETE" && path == "/api/activities" {
		q := r.URL.Query()
		for k, v := range q {
			if (k != "id" && k != "manualId") || len(v) != 1 {
				failure(w, ErrInvalid)
				return
			}
		}
		if e := s.Repository.Delete(ctx, actor, q.Get("id"), q.Get("manualId")); e != nil {
			failure(w, e)
			return
		}
		send(w, 204, nil)
		return
	}
	w.Header().Set("Allow", "GET, POST, PUT, DELETE")
	w.WriteHeader(405)
}
