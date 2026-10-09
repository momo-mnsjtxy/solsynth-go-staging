// SPDX-License-Identifier: AGPL-3.0-only
package friends

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

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func Handles(path string) bool {
	return path == "/api/friends/overview" || path == "/passport/friends/overview"
}
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, e error) {
	var a *authority.Failure
	var f *Failure
	if errors.As(e, &a) {
		send(w, a.Status, a)
	} else if errors.As(e, &f) {
		send(w, f.Status, f)
	} else {
		send(w, 503, ErrUnavailable)
	}
}
func parse(raw string) (Filter, error) {
	f := Filter{Take: 1000}
	q, e := url.ParseQuery(raw)
	if e != nil {
		return f, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 || v[0] == "" {
			return f, ErrInvalid
		}
		switch k {
		case "includeOffline":
			f.IncludeOffline, e = strconv.ParseBool(v[0])
		case "offset", "take":
			f.Paged = true
		default:
			return f, ErrInvalid
		}
		if e != nil {
			return f, ErrInvalid
		}
	}
	if f.Paged {
		f.Take = 20
	}
	for k, p := range map[string]*int{"offset": &f.Offset, "take": &f.Take} {
		if v := q.Get(k); v != "" {
			*p, e = strconv.Atoi(v)
			if e != nil {
				return f, ErrInvalid
			}
		}
	}
	return f, Validate(f)
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !Handles(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		return
	}
	if s == nil || s.Repository == nil || s.Authority == nil || s.Bind == nil {
		failure(w, ErrUnavailable)
		return
	}
	if r.URL.User != nil {
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
	f, e := parse(r.URL.RawQuery)
	if e != nil {
		failure(w, e)
		return
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		failure(w, e)
		return
	}
	items, total, e := s.Repository.Overview(s.Bind(r.Context(), p), p.Account.ID, f)
	if e != nil {
		failure(w, e)
		return
	}
	if items == nil {
		items = []Item{}
	}
	if f.Paged {
		w.Header().Set("X-Total", strconv.Itoa(total))
	}
	send(w, 200, items)
}
