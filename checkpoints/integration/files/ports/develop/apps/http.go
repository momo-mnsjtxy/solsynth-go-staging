// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/passport/board"
	"strconv"
	"strings"
)

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func (s *Service) AppHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAppCredentialRoute(r) {
			http.NotFound(w, r)
			return
		}
		s.appCredential(w, r)
	})
}
func respond(w http.ResponseWriter, status int, v any) {
	raw, e := json.Marshal(v)
	if e != nil {
		http.Error(w, "response unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != 204 {
		_, _ = w.Write(append(raw, '\n'))
	}
}
func failure(w http.ResponseWriter, e error) {
	status, code := 503, "DEV_APP_UNAVAILABLE"
	var f *authority.Failure
	switch {
	case errors.As(e, &f):
		status, code = f.Status, f.Code
	case errors.Is(e, ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(e, ErrNotFound):
		status, code = 404, "DEV_APP_NOT_FOUND"
	case errors.Is(e, ErrConflict):
		status, code = 409, "DEV_APP_CONFLICT"
	case errors.Is(e, ErrProvider):
		status, code = 501, "DEV_APP_PROVIDER_UNAVAILABLE"
	case errors.Is(e, ErrInvalid), errors.Is(e, develop.ErrInvalidQuery), errors.Is(e, board.ErrInvalidOperation):
		status, code = 400, "DEV_APP_REQUEST_INVALID"
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="apps"`)
	}
	respond(w, status, map[string]any{"code": code, "status": status})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return ErrInvalid
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.UseNumber()
	var raw json.RawMessage
	if e := d.Decode(&raw); e != nil || len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalid
	}
	var x any
	if e := d.Decode(&x); e != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrInvalid
	}
	return nil
}
func query(r *http.Request) (url.Values, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, ErrInvalid
	}
	for _, v := range q {
		if len(v) != 1 {
			return nil, ErrInvalid
		}
	}
	return q, nil
}
func (s *Service) actor(r *http.Request) (string, error) {
	if s.Authority == nil {
		return "", authority.ErrUnavailable
	}
	p, e := s.Authority.Require(r.Context())
	if e != nil {
		return "", e
	}
	if !authority.ValidID(p.Account.ID) {
		return "", authority.ErrInvalid
	}
	return p.Account.ID, nil
}
func method(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	respond(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
}
func one[T any](w http.ResponseWriter, status int, values []T, err error, single bool) {
	if err != nil {
		failure(w, err)
		return
	}
	if status == 204 {
		w.WriteHeader(204)
		return
	}
	if single {
		if len(values) != 1 {
			failure(w, ErrNotFound)
			return
		}
		respond(w, status, values[0])
		return
	}
	respond(w, status, values)
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	if s.serveQuota(w, r) {
		return
	}
	if IsAppCredentialRoute(r) {
		s.appCredential(w, r)
		return
	}
	q, e := query(r)
	if e != nil {
		failure(w, e)
		return
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) < 2 || p[0] != "api" {
		http.NotFound(w, r)
		return
	}
	if s.serveManagement(w, r, p, q) {
		return
	}
	if p[1] == "apps" {
		s.public(w, r, p, q)
		return
	}
	if p[1] == "developers" {
		if len(p) == 3 && r.Method == "GET" {
			v, e := s.Repository.Developers(r.Context(), "", p[2], false)
			one(w, 200, v, e, true)
			return
		}
		actor, e := s.actor(r)
		if e != nil {
			failure(w, e)
			return
		}
		if len(p) == 2 && r.Method == "GET" {
			v, e := s.Repository.Developers(r.Context(), actor, "", false)
			w.Header().Set("X-Total", strconv.Itoa(len(v)))
			one(w, 200, v, e, false)
			return
		}
		if len(p) == 4 && p[3] == "enroll" && r.Method == "POST" {
			v, e := s.Repository.Developers(r.Context(), actor, p[2], true)
			one(w, 200, v, e, true)
			return
		}
		method(w, "GET, POST")
		return
	}
	if len(p) < 3 || p[1] != "private" {
		http.NotFound(w, r)
		return
	}
	actor, e := s.actor(r)
	if e != nil {
		failure(w, e)
		return
	}
	scope := Scope{Actor: actor, Developer: q.Get("dev"), Project: q.Get("proj")}
	if scope.Developer == "" {
		failure(w, ErrInvalid)
		return
	}
	if p[2] == "projects" && len(p) <= 4 {
		ref := ""
		if len(p) == 4 {
			ref = p[3]
		}
		action := "read"
		var req *ProjectPatch
		status := 200
		switch r.Method {
		case "GET":
		case "POST":
			if ref != "" {
				method(w, "GET, PUT, DELETE")
				return
			}
			action = "create"
			status = 201
			req = &ProjectPatch{}
		case "PUT":
			if ref == "" {
				method(w, "GET, POST")
				return
			}
			action = "update"
			req = &ProjectPatch{}
		case "DELETE":
			if ref == "" {
				method(w, "GET, POST")
				return
			}
			action = "delete"
			status = 204
		default:
			method(w, "GET, POST, PUT, DELETE")
			return
		}
		if req != nil {
			if e = decode(w, r, req); e != nil {
				failure(w, e)
				return
			}
		}
		v, e := s.Repository.Projects(r.Context(), scope, ref, req, action)
		if e == nil && status == 201 {
			w.Header().Set("Location", "/api/private/projects/"+v[0].ID+"?dev="+url.QueryEscape(scope.Developer))
		}
		one(w, status, v, e, ref != "" || action == "create")
		return
	}
	if p[2] != "apps" {
		http.NotFound(w, r)
		return
	}
	if len(p) >= 4 {
		scope.App = p[3]
		if !authority.ValidID(scope.App) {
			failure(w, ErrInvalid)
			return
		}
	}
	if !authority.ValidID(scope.Project) {
		failure(w, ErrInvalid)
		return
	}
	if len(p) <= 4 {
		action := "read"
		var req *AppPatch
		status := 200
		switch r.Method {
		case "GET":
		case "POST":
			if scope.App != "" {
				method(w, "GET, PATCH, DELETE")
				return
			}
			action = "create"
			status = 201
			req = &AppPatch{}
		case "PATCH":
			if scope.App == "" {
				method(w, "GET, POST")
				return
			}
			action = "update"
			req = &AppPatch{}
		case "DELETE":
			if scope.App == "" {
				method(w, "GET, POST")
				return
			}
			action = "delete"
			status = 204
		default:
			method(w, "GET, POST, PATCH, DELETE")
			return
		}
		if req != nil {
			if e = decode(w, r, req); e != nil {
				failure(w, e)
				return
			}
		}
		v, e := s.Repository.Apps(r.Context(), scope, req, action)
		if e == nil && status == 201 {
			w.Header().Set("Location", "/api/private/apps/"+v[0].ID+"?dev="+url.QueryEscape(scope.Developer)+"&proj="+scope.Project)
		}
		one(w, status, v, e, scope.App != "" || action == "create")
		return
	}
	if len(p) > 7 {
		http.NotFound(w, r)
		return
	}
	ref := ""
	if len(p) > 5 {
		ref = p[5]
	}
	switch p[4] {
	case "products":
		if ref != "" && !authority.ValidID(ref) {
			failure(w, ErrInvalid)
			return
		}
		if len(p) > 6 {
			http.NotFound(w, r)
			return
		}
		action, status, ok := mutation(r.Method, ref, "PATCH")
		if !ok {
			method(w, "GET, POST, PATCH, DELETE")
			return
		}
		var req *ProductPatch
		if action == "create" || action == "update" {
			req = &ProductPatch{}
			if e = decode(w, r, req); e != nil {
				failure(w, e)
				return
			}
		}
		v, e := s.Repository.Products(r.Context(), scope, ref, req, action)
		if e == nil && status == 201 {
			w.Header().Set("Location", "/api/private/apps/"+scope.App+"/products/"+v[0].ID+"?dev="+url.QueryEscape(scope.Developer)+"&proj="+scope.Project)
		}
		one(w, status, v, e, ref != "" || action == "create")
	case "board":
		if len(p) > 6 {
			http.NotFound(w, r)
			return
		}
		action, status, ok := mutation(r.Method, ref, "PUT")
		if !ok {
			method(w, "GET, POST, PUT, DELETE")
			return
		}
		var req *board.Manifest
		if action == "create" || action == "update" {
			req = &board.Manifest{IsEnabled: true, PayloadType: "object", RendererType: "data"}
			if e = decode(w, r, req); e != nil {
				failure(w, e)
				return
			}
		}
		v, e := s.Repository.Widgets(r.Context(), scope, ref, req, action)
		if status == 201 {
			status = 200
		}
		one(w, status, v, e, ref != "" || action == "create")
	case "secrets":
		action, status, ok := mutation(r.Method, ref, "")
		if len(p) == 7 && p[6] == "rotate" && r.Method == "POST" && ref != "" {
			action, status, ok = "rotate", 200, true
		} else if len(p) > 6 {
			http.NotFound(w, r)
			return
		}
		if !ok {
			method(w, "GET, POST, DELETE")
			return
		}
		var req *SecretRequest
		if action == "create" || action == "rotate" {
			req = &SecretRequest{}
			if r.ContentLength != 0 || action == "create" {
				if e = decode(w, r, req); e != nil {
					failure(w, e)
					return
				}
			}
		}
		v, e := s.Repository.Secrets(r.Context(), scope, ref, req, action)
		one(w, status, v, e, ref != "" || action == "create")
	default:
		http.NotFound(w, r)
	}
}
func mutation(method, ref, update string) (string, int, bool) {
	switch method {
	case "GET":
		return "read", 200, true
	case "POST":
		return "create", 201, ref == ""
	case "DELETE":
		return "delete", 204, ref != ""
	default:
		if update != "" && method == update {
			return "update", 200, ref != ""
		}
	}
	return "", 0, false
}
func (s *Service) public(w http.ResponseWriter, r *http.Request, p []string, q url.Values) {
	if len(p) == 4 && p[3] == "consent" {
		actor, e := s.actor(r)
		if e != nil {
			failure(w, e)
			return
		}
		var req *ConsentRequest
		switch r.Method {
		case "GET", "DELETE":
		case "PUT":
			req = &ConsentRequest{}
			if e = decode(w, r, req); e != nil {
				failure(w, e)
				return
			}
		default:
			method(w, "GET, PUT, DELETE")
			return
		}
		v, e := s.Repository.Consent(r.Context(), actor, p[2], req, r.Method == "DELETE")
		if e != nil {
			failure(w, e)
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(204)
		} else {
			respond(w, 200, v)
		}
		return
	}
	if r.Method != "GET" {
		method(w, "GET")
		return
	}
	if len(p) == 3 && p[2] == "board" {
		s.discoverBoard(w, r, q)
		return
	}
	if len(p) == 2 {
		query := develop.Query{Take: 20}
		for _, a := range []struct {
			k string
			n *int32
		}{{"take", &query.Take}, {"offset", &query.Offset}} {
			if v := q.Get(a.k); v != "" {
				n, e := strconv.ParseInt(v, 10, 32)
				if e != nil {
					failure(w, ErrInvalid)
					return
				}
				*a.n = int32(n)
			}
		}
		if v, ok := q["search"]; ok {
			query.Search = &v[0]
		}
		v, e := s.Repository.Discover(r.Context(), query)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.Itoa(int(v.Total)))
		respond(w, 200, v.Apps)
		return
	}
	if len(p) == 3 {
		v, e := s.Repository.PublicApp(r.Context(), p[2])
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, v)
		return
	}
	if (len(p) == 4 || len(p) == 5) && p[3] == "products" {
		ref := ""
		if len(p) == 5 {
			ref = p[4]
		}
		v, e := s.Repository.PublicProducts(r.Context(), p[2], ref)
		one(w, 200, v, e, ref != "")
		return
	}
	http.NotFound(w, r)
}
func appCredential(r *http.Request) (string, error) {
	if r.URL.RawQuery != "" || r.URL.User != nil {
		return "", authority.ErrInvalid
	}
	for k := range r.Header {
		lower := strings.ToLower(k)
		if lower == "cookie" || lower == "proxy-authorization" || lower == "x-account-id" || lower == "x-user-id" || strings.HasPrefix(lower, "x-test-") || strings.HasPrefix(lower, "x-synthetic-") {
			return "", authority.ErrInvalid
		}
	}
	auth := r.Header.Values("Authorization")
	secret := r.Header.Values("X-App-Secret")
	if len(auth)+len(secret) != 1 {
		return "", authority.ErrInvalid
	}
	if len(secret) == 1 {
		if strings.TrimSpace(secret[0]) != secret[0] || secret[0] == "" {
			return "", authority.ErrInvalid
		}
		return secret[0], nil
	}
	p := strings.Split(auth[0], " ")
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") || p[1] == "" {
		return "", authority.ErrInvalid
	}
	return p[1], nil
}
func (s *Service) appCredential(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if p[5] == "widgets" && r.Method != "GET" {
		method(w, "GET")
		return
	}
	if p[5] == "payload" && r.Method != "POST" {
		method(w, "POST")
		return
	}
	token, e := appCredential(r)
	if e != nil {
		failure(w, e)
		return
	}
	if p[5] == "widgets" {
		v, e := s.Repository.AppWidgets(r.Context(), p[3], token)
		one(w, 200, v, e, false)
		return
	}
	var req PayloadRequest
	if e = decode(w, r, &req); e != nil {
		failure(w, e)
		return
	}
	v, e := s.Repository.PushPayload(r.Context(), p[3], token, req)
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, v)
}
