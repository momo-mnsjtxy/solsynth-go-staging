// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// BearerToken accepts only one explicit Bearer header. Cookies, tk query
// parameters, X-Account-ID and reverse-proxy identity assertions are not trusted.
func BearerToken(r *http.Request) (string, error) {
	v := r.Header.Values("Authorization")
	if len(v) != 1 {
		return "", ErrInvalid
	}
	p := strings.Fields(v[0])
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") || len(p[1]) > 8192 {
		return "", ErrInvalid
	}
	return p[1], nil
}
func (s *Service) AuthenticateRequest(r *http.Request) (Principal, error) {
	token, e := BearerToken(r)
	if e != nil {
		return Principal{}, e
	}
	return s.Authenticate(r.Context(), token)
}
func (s *Service) AuthorizeRequest(r *http.Request, permissions ...string) (Principal, error) {
	token, e := BearerToken(r)
	if e != nil {
		return Principal{}, e
	}
	return s.Authorize(r.Context(), token, permissions...)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func writeError(w http.ResponseWriter, e error) {
	var f *Failure
	if !errors.As(known(e), &f) {
		f = ErrUnavailable
	}
	if f.Status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="authority"`)
	}
	respond(w, f.Status, f)
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stargate/accounts/me", func(w http.ResponseWriter, r *http.Request) {
		p, e := s.AuthenticateRequest(r)
		if e != nil {
			writeError(w, e)
			return
		}
		respond(w, 200, p.Account)
	})
	mux.HandleFunc("GET /stargate/sessions", func(w http.ResponseWriter, r *http.Request) {
		token, e := BearerToken(r)
		if e != nil {
			writeError(w, e)
			return
		}
		q := r.URL.Query()
		parse := func(name string, def int) (int, error) {
			if len(q[name]) == 0 {
				return def, nil
			}
			if len(q[name]) != 1 {
				return 0, ErrInvalid
			}
			return strconv.Atoi(q.Get(name))
		}
		offset, e := parse("offset", 0)
		if e != nil {
			respond(w, 400, &Failure{"BAD_REQUEST", "Invalid offset.", 400})
			return
		}
		take, e := parse("take", 50)
		if e != nil || offset < 0 || take < 1 || take > 200 {
			respond(w, 400, &Failure{"BAD_REQUEST", "Invalid pagination.", 400})
			return
		}
		children := false
		if v := q.Get("includeChildren"); v != "" {
			children, e = strconv.ParseBool(v)
			if e != nil {
				respond(w, 400, &Failure{"BAD_REQUEST", "Invalid includeChildren.", 400})
				return
			}
		}
		rows, total, e := s.Sessions(r.Context(), token, offset, take, children)
		if e != nil {
			writeError(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.Itoa(total))
		respond(w, 200, rows)
	})
	mux.HandleFunc("DELETE /stargate/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		token, e := BearerToken(r)
		if e == nil {
			e = s.Revoke(r.Context(), token, r.PathValue("id"))
		}
		if e != nil {
			writeError(w, e)
			return
		}
		respond(w, 204, nil)
	})
	mux.HandleFunc("POST /stargate/auth/token", s.firstPartyToken)
	mux.HandleFunc("POST /stargate/auth/open/token", s.oauthToken)
	return mux
}
func (s *Service) firstPartyToken(w http.ResponseWriter, r *http.Request) {
	m, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || m != "application/json" {
		respond(w, 415, &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		respond(w, 400, &Failure{"BAD_REQUEST", "Invalid token request.", 400})
		return
	}
	var req struct {
		Grant   string `json:"grant_type"`
		Refresh string `json:"refresh_token"`
		Code    string `json:"code"`
	}
	if decodeStrict(b, &req) != nil {
		respond(w, 400, &Failure{"BAD_REQUEST", "Invalid token request.", 400})
		return
	}
	var pair Pair
	switch req.Grant {
	case "refresh_token":
		if req.Code != "" {
			writeError(w, ErrInvalid)
			return
		}
		pair, e = s.Refresh(r.Context(), req.Refresh, 0, "")
	case "authorization_code":
		if req.Refresh != "" {
			writeError(w, ErrInvalid)
			return
		}
		pair, e = s.ExchangeCode(r.Context(), req.Code)
	default:
		respond(w, 400, &Failure{"UNSUPPORTED_GRANT_TYPE", "Unsupported grant type.", 400})
		return
	}
	if e != nil {
		writeError(w, e)
		return
	}
	respond(w, 200, pair)
}
func (s *Service) oauthToken(w http.ResponseWriter, r *http.Request) {
	fail := func(code string, status int) { respond(w, status, map[string]string{"error": code}) }
	m, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || m != "application/x-www-form-urlencoded" {
		fail("invalid_request", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		fail("invalid_request", 400)
		return
	}
	for k, v := range r.PostForm {
		if len(v) != 1 || k != "grant_type" && k != "refresh_token" && k != "client_id" {
			fail("invalid_request", 400)
			return
		}
	}
	if r.PostForm.Get("grant_type") != "refresh_token" {
		fail("unsupported_grant_type", 400)
		return
	}
	if r.PostForm.Get("client_id") == "" {
		fail("invalid_request", 400)
		return
	}
	p, e := s.Refresh(r.Context(), r.PostForm.Get("refresh_token"), 1, r.PostForm.Get("client_id"))
	if e != nil {
		if errors.Is(e, ErrUnavailable) {
			fail("temporarily_unavailable", 503)
		} else {
			fail("invalid_grant", 400)
		}
		return
	}
	respond(w, 200, map[string]any{"access_token": p.Token, "refresh_token": p.RefreshToken, "expires_in": p.ExpiresIn, "refresh_expires_in": p.RefreshExpiresIn, "token_type": "Bearer"})
}
