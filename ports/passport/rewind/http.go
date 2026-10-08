// SPDX-License-Identifier: AGPL-3.0-only
package rewind

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

type Authenticator interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}
type Service struct {
	Repository Repository
	Authority  Authenticator
}

func RouteMatches(path string) bool {
	return path == "/api/rewind" || strings.HasPrefix(path, "/api/rewind/") || path == "/stargate/rewind" || strings.HasPrefix(path, "/stargate/rewind/")
}
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, e error) {
	e = Normalize(e)
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) {
		reply(w, f.Status, f)
	} else if errors.As(e, &a) {
		reply(w, a.Status, a)
	} else {
		reply(w, 503, ErrUnavailable)
	}
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	if !RouteMatches(r.URL.Path) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, authority.ErrForbidden)
			return
		}
	}
	parts := strings.Split(strings.TrimPrefix(strings.Replace(r.URL.Path, "/stargate/", "/api/", 1), "/api/rewind/"), "/")
	if len(parts) == 1 && parts[0] != "me" && r.Method == "GET" {
		if len(r.Header.Values("Authorization")) != 0 {
			if s.Authority == nil {
				failure(w, ErrUnavailable)
				return
			}
			if _, e := s.Authority.AuthenticateRequest(r); e != nil {
				failure(w, e)
				return
			}
		}
		if len(parts[0]) != 32 {
			failure(w, ErrNotFound)
			return
		}
		v, e := s.Repository.Public(r.Context(), parts[0])
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, v)
		return
	}
	if s.Authority == nil {
		failure(w, ErrUnavailable)
		return
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		failure(w, e)
		return
	}
	ctx := r.Context()
	if b, ok := s.Repository.(interface {
		BindAuthority(context.Context, authority.Principal) context.Context
	}); ok {
		ctx = b.BindAuthority(ctx, p)
	}
	if len(parts) == 1 && parts[0] == "me" && r.Method == "GET" {
		v, e := s.Repository.Current(ctx, p.Account.ID)
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, v)
		return
	}
	if len(parts) == 3 && parts[0] == "me" && r.Method == "POST" && (parts[2] == "public" || parts[2] == "private") {
		year, e := strconv.Atoi(parts[1])
		if e != nil {
			failure(w, ErrInvalid)
			return
		}
		v, e := s.Repository.Share(ctx, p.Account.ID, year, parts[2] == "public")
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, v)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}
