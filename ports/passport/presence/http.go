// SPDX-License-Identifier: AGPL-3.0-only
package presence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func RouteMatches(path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return len(p) == 4 && p[0] == "api" && p[1] == "accounts" && p[3] == "statuses" || strings.HasPrefix(path, "/api/accounts/me/presence")
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var a *authority.Failure
	if errors.As(e, &a) {
		send(w, a.Status, a)
		return
	}
	var f *Failure
	if errors.As(e, &f) {
		send(w, f.Status, f)
		return
	}
	send(w, 503, ErrUnavailable)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return ErrInvalid
	}
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
	return nil
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if !RouteMatches(path) {
		w.WriteHeader(404)
		return
	}
	authPresent := false
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			authPresent = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, ErrForbidden)
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
	parts := strings.Split(strings.Trim(path, "/"), "/")
	public := len(parts) == 4 && parts[2] != "me" && parts[3] == "statuses" && r.Method == "GET"
	actor := ""
	ctx := r.Context()
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
	if len(parts) == 4 && parts[3] == "statuses" {
		target := parts[2]
		if target == "me" {
			target = actor
		}
		switch r.Method {
		case "GET":
			v, e := s.Repository.GetStatus(ctx, actor, target)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		case "POST", "PATCH":
			if parts[2] != "me" {
				w.WriteHeader(405)
				return
			}
			var v StatusRequest
			if e := decode(w, r, &v); e != nil {
				failure(w, e)
				return
			}
			out, e := s.Repository.SetStatus(ctx, actor, v, r.Method == "PATCH")
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, out)
		case "DELETE":
			if parts[2] != "me" {
				w.WriteHeader(405)
				return
			}
			if e := s.Repository.ClearStatus(ctx, actor, r.URL.Query().Get("app")); e != nil {
				failure(w, e)
				return
			}
			send(w, 204, nil)
		default:
			w.Header().Set("Allow", "GET, POST, PATCH, DELETE")
			w.WriteHeader(405)
		}
		return
	}
	switch path {
	case "/api/accounts/me/presence/preferences":
		switch r.Method {
		case "GET":
			v, e := s.Repository.GetPreferences(ctx, actor)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		case "PUT":
			var v Preferences
			if e := decode(w, r, &v); e != nil {
				failure(w, e)
				return
			}
			out, e := s.Repository.SetPreferences(ctx, actor, v)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, out)
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(405)
		}
	case "/api/accounts/me/presence/heartbeat":
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return
		}
		var v struct {
			IsIdle bool `json:"is_idle"`
		}
		if e := decode(w, r, &v); e != nil {
			failure(w, e)
			return
		}
		out, e := s.Repository.Heartbeat(ctx, actor, v.IsIdle)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
	case "/api/accounts/me/presence":
		if r.Method != "DELETE" {
			w.Header().Set("Allow", "DELETE")
			w.WriteHeader(405)
			return
		}
		if e := s.Repository.Disconnect(ctx, actor); e != nil {
			failure(w, e)
			return
		}
		send(w, 204, nil)
	default:
		w.WriteHeader(404)
	}
}
