// SPDX-License-Identifier: AGPL-3.0-only
package history

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func route(p string) (ref, operation string, ok bool) {
	pieces := strings.Split(strings.TrimPrefix(p, "/api/accounts/"), "/")
	if !strings.HasPrefix(p, "/api/accounts/") || len(pieces) < 2 || pieces[0] == "" {
		return
	}
	ref = pieces[0]
	if len(pieces) == 2 && (pieces[1] == "timeline" || pieces[1] == "credits" || ref == "me" && (pieces[1] == "actions" || pieces[1] == "connections")) {
		return ref, pieces[1], true
	}
	if len(pieces) == 3 && ref == "me" && pieces[1] == "credits" && pieces[2] == "history" {
		return ref, "credit-history", true
	}
	return "", "", false
}
func Handles(p string) bool { _, _, ok := route(p); return ok }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	var x *activities.Failure
	switch {
	case errors.As(e, &f):
		send(w, f.Status, f)
	case errors.As(e, &a):
		send(w, a.Status, a)
	case errors.As(e, &x):
		send(w, x.Status, x)
	default:
		send(w, 503, ErrUnavailable)
	}
}
func parse(q url.Values, operation string) (f Filter, e error) {
	f.Take = 20
	allowed := map[string]bool{"take": true, "offset": true, "from": true, "to": true}
	if operation == "actions" {
		allowed["action"] = true
	}
	if operation == "connections" {
		allowed = map[string]bool{"provider": true}
	}
	if operation == "credits" {
		allowed = map[string]bool{}
	}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 || v[0] == "" {
			return f, ErrInvalid
		}
	}
	for k, p := range map[string]*int{"take": &f.Take, "offset": &f.Offset} {
		if v := q.Get(k); v != "" {
			*p, e = strconv.Atoi(v)
			if e != nil {
				return f, ErrInvalid
			}
		}
	}
	// Preserve the source actions endpoint's lower-bound normalization.
	if operation == "actions" {
		f.Take = max(1, f.Take)
		f.Offset = max(0, f.Offset)
	}
	for k, p := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(k); v != "" {
			t, x := time.Parse(time.RFC3339Nano, v)
			if x != nil || t.Year() < 1 || t.Year() > 9999 {
				return f, ErrInvalid
			}
			u := t.UTC()
			*p = &u
		}
	}
	f.Action = q.Get("action")
	return f, Validate(f)
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	ref, operation, ok := route(r.URL.Path)
	if !ok {
		w.WriteHeader(404)
		return
	}
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != r.URL.Path {
		failure(w, ErrInvalid)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return
	}
	if r.Method == "HEAD" {
		w = headWriter{w}
	}
	authPresent := false
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			authPresent = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-app-secret" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, authority.ErrForbidden)
			return
		}
	}
	if r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	f, e := parse(r.URL.Query(), operation)
	if e != nil {
		failure(w, e)
		return
	}
	ctx := r.Context()
	actor := ""
	if ref == "me" || authPresent {
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
	if s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	if ref == "me" {
		ref = actor
	}
	var result any
	total := -1
	switch operation {
	case "timeline":
		result, total, e = s.Repository.Timeline(ctx, actor, ref, f)
	case "actions":
		result, total, e = s.Repository.Actions(ctx, actor, f)
	case "credits":
		result, e = s.Repository.Credits(ctx, actor, ref)
	case "credit-history":
		result, total, e = s.Repository.CreditHistory(ctx, actor, f)
	case "connections":
		provider := r.URL.Query().Get("provider")
		if len(provider) > 128 {
			failure(w, ErrInvalid)
			return
		}
		result, e = s.Repository.Connections(ctx, actor, provider)
	}
	if e != nil {
		failure(w, e)
		return
	}
	if total >= 0 {
		w.Header().Set("X-Total", strconv.Itoa(total))
	}
	send(w, 200, result)
}

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(b []byte) (int, error) { return len(b), nil }
