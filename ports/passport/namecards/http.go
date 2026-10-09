// SPDX-License-Identifier: AGPL-3.0-only
package namecards

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
	"strconv"
	"strings"
	"unicode/utf8"
)

const pathPrefix = "/api/accounts/me/name-change-card"

func RouteMatches(path string) bool {
	return path == pathPrefix || strings.HasPrefix(path, pathPrefix+"/") || path == "/stargate/accounts/me/name-change-card" || strings.HasPrefix(path, "/stargate/accounts/me/name-change-card/")
}

type Authenticator interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}
type Service struct {
	Repository Repository
	Authority  Authenticator
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
	path := strings.Replace(r.URL.Path, "/stargate/", "/api/", 1)
	if len(r.Header.Values("Idempotency-Key")) > 1 || !ValidKey(r.Header.Get("Idempotency-Key")) {
		failure(w, ErrInvalid)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	if path == pathPrefix && r.Method == "GET" {
		take, offset := 100, 0
		for k, v := range q {
			if len(v) != 1 || (k != "take" && k != "offset") {
				failure(w, ErrInvalid)
				return
			}
			n, e := strconv.Atoi(v[0])
			if e != nil || n < 0 || k == "take" && (n < 1 || n > 200) || k == "offset" && n > 1000000 {
				failure(w, ErrInvalid)
				return
			}
			if k == "take" {
				take = n
			} else {
				offset = n
			}
		}
		out, total, e := s.Repository.List(ctx, p.Account.ID, take, offset)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
		reply(w, 200, out)
		return
	}
	if len(q) != 0 {
		failure(w, ErrInvalid)
		return
	}
	if path == pathPrefix+"/order" && r.Method == "POST" {
		out, e := s.Repository.Purchase(ctx, p.Account.ID, key)
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, out)
		return
	}
	if path == pathPrefix+"/use" && r.Method == "POST" {
		typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || typ != "application/json" {
			failure(w, ErrInvalid)
			return
		}
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if e != nil {
			failure(w, ErrInvalid)
			return
		}
		request, e := decodeUse(b)
		if e != nil {
			failure(w, e)
			return
		}
		out, e := s.Repository.Use(ctx, p.Account.ID, request, key)
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, out)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}
func decodeUse(b []byte) (q UseRequest, e error) {
	if len(b) > 8192 || !utf8.Valid(b) {
		return q, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return q, ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		token, e = d.Token()
		k, ok := token.(string)
		if e != nil || !ok || seen[k] {
			return q, ErrInvalid
		}
		seen[k] = true
		switch k {
		case "target":
			e = d.Decode(&q.Target)
		case "target_id":
			e = d.Decode(&q.TargetID)
		case "new_name":
			e = d.Decode(&q.NewName)
		default:
			return q, ErrInvalid
		}
		if e != nil {
			return q, ErrInvalid
		}
	}
	if token, e = d.Token(); e != nil || token != json.Delim('}') {
		return q, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return q, ErrInvalid
	}
	return q, ValidateUse(&q)
}
