// SPDX-License-Identifier: AGPL-3.0-only
package accountsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"strings"
	"unicode/utf8"
)

const preferencePath = "/api/accounts/me/notification-preferences"

func RouteMatches(path string) bool {
	return path == preferencePath || strings.HasPrefix(path, preferencePath+"/")
}

type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}
type Service struct {
	Repository Repository
	Authority  Authority
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
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
	if !RouteMatches(r.URL.Path) || r.URL.RawPath != "" || r.URL.User != nil {
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
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	for k, v := range q {
		if k != "app" || len(v) != 1 {
			failure(w, ErrInvalid)
			return
		}
	}
	app := q.Get("app")
	if app == "" {
		app = notifications.DefaultApp
	}
	if !notifications.ValidApp(app) {
		failure(w, ErrInvalid)
		return
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		failure(w, e)
		return
	}
	ctx := r.Context()
	if binder, ok := s.Repository.(interface {
		BindAuthority(context.Context, authority.Principal) context.Context
	}); ok {
		ctx = binder.BindAuthority(ctx, p)
	}
	if r.URL.Path == preferencePath && r.Method == "GET" {
		out, e := s.Repository.List(ctx, p.Account.ID, app)
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, out)
		return
	}
	topic := strings.TrimPrefix(r.URL.Path, preferencePath+"/")
	if !ValidTopic(topic) {
		failure(w, ErrInvalid)
		return
	}
	switch r.Method {
	case "PUT":
		typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || typ != "application/json" {
			failure(w, ErrInvalid)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if e != nil || !utf8.Valid(raw) {
			failure(w, ErrInvalid)
			return
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		token, e := d.Token()
		if e != nil || token != json.Delim('{') {
			failure(w, ErrInvalid)
			return
		}
		seen := map[string]bool{}
		v := PreferenceInput{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				failure(w, ErrInvalid)
				return
			}
			k, ok := key.(string)
			if !ok || seen[k] || (k != "preference" && k != "description") {
				failure(w, ErrInvalid)
				return
			}
			seen[k] = true
			target := &v.Description
			if k == "preference" {
				target = &v.Preference
			}
			if e = d.Decode(target); e != nil {
				failure(w, ErrInvalid)
				return
			}
		}
		if token, e = d.Token(); e != nil || token != json.Delim('}') {
			failure(w, ErrInvalid)
			return
		}
		if _, e = d.Token(); e != io.EOF {
			failure(w, ErrInvalid)
			return
		}
		out, e := s.Repository.Set(ctx, p.Account.ID, app, topic, v)
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, out)
	case "DELETE":
		if e = s.Repository.Reset(ctx, p.Account.ID, app, topic); e != nil {
			failure(w, e)
			return
		}
		reply(w, 204, nil)
	default:
		w.WriteHeader(405)
	}
}
