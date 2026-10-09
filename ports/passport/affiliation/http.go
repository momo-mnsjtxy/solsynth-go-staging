// SPDX-License-Identifier: AGPL-3.0-only
package affiliation

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

type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
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
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var f *authority.Failure
	if !errors.As(e, &f) {
		f = ErrUnavailable
	}
	reply(w, f.Status, f)
}
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil || s.Authority == nil {
		failure(w, ErrUnavailable)
		return
	}
	if r.URL.User != nil || !(r.URL.Path == "/api/affiliations" || strings.HasPrefix(r.URL.Path, "/api/affiliations/")) {
		failure(w, ErrNotFound)
		return
	}
	authPresent := false
	if path.Clean(r.URL.Path) != r.URL.Path || strings.ContainsAny(r.URL.Path, "\\\x00") || r.URL.RawPath != "" {
		failure(w, ErrInvalid)
		return
	}
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
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	for k, v := range q {
		if len(v) != 1 || (k != "order" && k != "desc" && k != "take" && k != "offset") {
			failure(w, ErrInvalid)
			return
		}
	}
	c := Command{Query: Query{Take: 10, Order: q.Get("order")}}
	for k, p := range map[string]*int{"take": &c.Query.Take, "offset": &c.Query.Offset} {
		if v := q.Get(k); v != "" {
			*p, e = strconv.Atoi(v)
			if e != nil {
				failure(w, ErrInvalid)
				return
			}
		}
	}
	if v := q.Get("desc"); v != "" {
		c.Query.Desc, e = strconv.ParseBool(v)
		if e != nil {
			failure(w, ErrInvalid)
			return
		}
	}
	if c.Query.Take < 1 || c.Query.Take > 100 || c.Query.Offset < 0 || c.Query.Offset > 100000 {
		failure(w, ErrInvalid)
		return
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/affiliations"), "/")
	switch {
	case r.URL.Path == "/api/affiliations" && r.Method == "GET":
		c.Operation = "list"
	case r.URL.Path == "/api/affiliations" && r.Method == "POST":
		c.Operation = "create"
		c.Create = &Create{}
		_, e = decode(w, r, c.Create)
	case r.URL.Path == "/api/affiliations/registration-invites" && r.Method == "POST":
		c.Operation = "invite.create"
		v := InviteCreate{}
		_, e = decode(w, r, &v)
		c.Create = &v.Create
		c.ExpiresAt = v.ExpiresAt
	case r.URL.Path == "/api/affiliations/registration-invites/consume" && r.Method == "POST":
		c.Operation = "consume"
		var v struct {
			Spell string `json:"spell"`
		}
		_, e = decode(w, r, &v)
		c.Word = strings.TrimSpace(v.Spell)
	case r.URL.Path == "/api/affiliations/purchase" && r.Method == "POST":
		c.Operation = "purchase"
	case len(p) == 2 && authority.ValidID(p[1]) && (r.Method == "GET" || r.Method == "DELETE"):
		c.ID = p[1]
		if r.Method == "GET" {
			c.Operation = "get"
		} else {
			c.Operation = "delete"
		}
	case len(p) == 3 && p[2] == "results" && r.Method == "GET" && authority.ValidID(p[1]):
		c.Operation = "results"
		c.ID = p[1]
	case len(p) == 3 && p[2] == "results" && r.Method == "POST":
		c.Operation = "record"
		c.Word = p[1]
		var v struct {
			Resource string `json:"resource_identifier"`
		}
		_, e = decode(w, r, &v)
		c.Resource = v.Resource
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		failure(w, e)
		return
	}
	actor := ""
	principal, e := s.Authority.AuthenticateRequest(r)
	if e == nil {
		principal, e = s.Authority.AuthenticatePrincipal(r.Context(), principal)
		if e != nil {
			failure(w, e)
			return
		}
		actor = principal.Account.ID
		r = r.WithContext(s.Repository.BindAuthority(r.Context(), principal))
	} else if c.Operation != "record" || authPresent || !errors.Is(e, authority.ErrInvalid) {
		failure(w, e)
		return
	}
	out, e := s.Repository.Execute(r.Context(), actor, c)
	if e != nil {
		failure(w, e)
		return
	}
	if out.Total != nil {
		w.Header().Set("X-Total", strconv.FormatInt(*out.Total, 10))
	}
	reply(w, 200, out.Value)
}
func decode(w http.ResponseWriter, r *http.Request, v any) ([]byte, error) {
	typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" {
		return nil, &authority.Failure{Code: "UNSUPPORTED_MEDIA_TYPE", Message: "JSON is required.", Status: 415}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if e != nil || !utf8.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = unique(d, 0); e != nil {
		return nil, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return nil, ErrInvalid
	}
	return b, nil
}
func unique(d *json.Decoder, depth int) error {
	if depth > 24 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	delimiter, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := token.(string)
			if !ok || seen[strings.ToLower(key)] {
				return ErrInvalid
			}
			seen[strings.ToLower(key)] = true
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
