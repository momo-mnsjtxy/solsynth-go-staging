// SPDX-License-Identifier: AGPL-3.0-only
package fortunes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
}
type Service struct {
	Repository              Repository
	Authority               Authority
	AllowAnonymousCatalogue bool
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
	if s == nil || s.Repository == nil || s.Authority == nil {
		failure(w, ErrUnavailable)
		return
	}
	if r.Method != "GET" || (r.URL.Path != "/api/fortune" && r.URL.Path != "/api/fortune/random") {
		reply(w, 404, ErrNotFound)
		return
	}
	if r.URL.User != nil || r.URL.EscapedPath() != r.URL.Path {
		failure(w, ErrInvalid)
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	random := strings.HasSuffix(r.URL.Path, "/random")
	for k, v := range q {
		if len(v) != 1 || k != "language" && k != "offset" && k != "take" || random && (k == "offset" || k == "take") {
			failure(w, ErrInvalid)
			return
		}
	}
	offset, take := 0, 50
	for name, p := range map[string]*int{"offset": &offset, "take": &take} {
		if q.Has(name) {
			*p, e = strconv.Atoi(q.Get(name))
			if e != nil {
				failure(w, ErrInvalid)
				return
			}
		}
	}
	present := false
	for k := range r.Header {
		lower := strings.ToLower(k)
		if lower == "authorization" {
			present = true
		}
		if lower == "x-account-id" || lower == "x-user-id" || lower == "x-role" || lower == "proxy-authorization" || strings.HasPrefix(lower, "x-test-") || strings.HasPrefix(lower, "x-synthetic-") {
			failure(w, authority.ErrForbidden)
			return
		}
	}
	actor := ""
	p, e := s.Authority.AuthenticateRequest(r)
	if e == nil {
		p, e = s.Authority.AuthenticatePrincipal(r.Context(), p)
		if e != nil {
			failure(w, e)
			return
		}
		actor = p.Account.ID
		if binder, ok := s.Repository.(interface {
			BindAuthority(context.Context, authority.Principal) context.Context
		}); ok {
			r = r.WithContext(binder.BindAuthority(r.Context(), p))
		}
	} else if present || !s.AllowAnonymousCatalogue || !errors.Is(e, authority.ErrInvalid) {
		failure(w, e)
		return
	}
	list, total, e := s.Repository.Catalogue(r.Context(), actor, q.Get("language"), offset, take, random)
	if e != nil {
		failure(w, e)
		return
	}
	if !random {
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
	}
	reply(w, 200, list)
}
