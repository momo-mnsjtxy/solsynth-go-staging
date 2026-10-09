// SPDX-License-Identifier: AGPL-3.0-only
package oauth

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

func (s *Service) serveExtension(w http.ResponseWriter, r *http.Request, p string) bool {
	switch p {
	case "/auth/open/introspect":
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return true
		}
		values, e := parameters(r, "token", "token_type_hint", "client_id", "client_secret")
		if e != nil {
			fail(w, e)
			return true
		}
		client, secret, e := credentials(r, values)
		if e != nil {
			fail(w, e)
			return true
		}
		repo, ok := s.Repository.(IntrospectionRepository)
		if !ok {
			fail(w, ErrUnavailable)
			return true
		}
		if values.Get("token") == "" {
			fail(w, ErrRequest)
			return true
		}
		out, e := repo.Introspect(r.Context(), client, secret, values.Get("token"))
		if e != nil {
			fail(w, e)
		} else {
			respond(w, 200, out)
		}
		return true
	case "/auth/open/device":
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return true
		}
		values, e := parameters(r, "scope", "client_id", "client_secret")
		if e != nil {
			fail(w, e)
			return true
		}
		client, secret, e := credentials(r, values)
		if e != nil {
			fail(w, e)
			return true
		}
		repo, ok := s.Repository.(DeviceRepository)
		if !ok {
			fail(w, ErrUnavailable)
			return true
		}
		out, e := repo.DeviceAuthorize(r.Context(), client, secret, values.Get("scope"))
		if e != nil {
			fail(w, e)
		} else {
			respond(w, 200, out)
		}
		return true
	case "/auth/open/device/verify":
		if r.Method != "POST" && r.Method != "GET" {
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(405)
			return true
		}
		values, e := parameters(r, "user_code", "authorize")
		if e != nil {
			fail(w, e)
			return true
		}
		if r.Method == "GET" && values.Has("authorize") {
			fail(w, ErrRequest)
			return true
		}
		p, e := s.Verifier.AuthenticateRequest(r)
		if e != nil {
			fail(w, e)
			return true
		}
		if p.Session.Type != 0 {
			fail(w, ErrDenied)
			return true
		}
		repo, ok := s.Repository.(DeviceRepository)
		if !ok {
			fail(w, ErrUnavailable)
			return true
		}
		ctx := s.Bind(r.Context(), p)
		if r.Method == "GET" {
			out, e := repo.DevicePreview(ctx, p.Account.ID, values.Get("user_code"))
			if e != nil {
				fail(w, e)
			} else {
				respond(w, 200, out)
			}
			return true
		}
		if values.Get("authorize") != "true" && values.Get("authorize") != "false" {
			fail(w, ErrRequest)
			return true
		}
		e = repo.DeviceDecide(ctx, p.Account.ID, values.Get("user_code"), values.Get("authorize") == "true")
		if e != nil {
			fail(w, e)
		} else {
			respond(w, 204, nil)
		}
		return true
	case "/auth/open/register":
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return true
		}
		if r.URL.RawQuery != "" {
			fail(w, ErrRequest)
			return true
		}
		typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || typ != "application/json" {
			fail(w, ErrRequest)
			return true
		}
		p, e := s.Verifier.AuthenticateRequest(r)
		if e != nil {
			fail(w, e)
			return true
		}
		if p.Session.Type != 0 {
			fail(w, ErrDenied)
			return true
		}
		var req Registration
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if e = dec.Decode(&req); e != nil {
			fail(w, ErrRequest)
			return true
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			fail(w, ErrRequest)
			return true
		}
		repo, ok := s.Repository.(RegistrationRepository)
		if !ok {
			fail(w, ErrUnavailable)
			return true
		}
		out, e := repo.Register(s.Bind(r.Context(), p), p.Account.ID, req)
		if e != nil {
			fail(w, e)
		} else {
			respond(w, 201, out)
		}
		return true
	}
	return false
}
