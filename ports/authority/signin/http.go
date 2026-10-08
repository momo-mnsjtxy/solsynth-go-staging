// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var f *authority.Failure
	if !errors.As(known(e), &f) {
		f = authority.ErrUnavailable
	}
	if f.Status == 429 {
		w.Header().Set("Retry-After", "600")
	}
	respond(w, f.Status, f)
}
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" {
		return &authority.Failure{Code: "UNSUPPORTED_MEDIA_TYPE", Message: "JSON is required.", Status: 415}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if e != nil {
		return ErrInput
	}
	// Request objects contain scalar fields. Reject duplicate keys as well as
	// unknown fields, trailing JSON and oversized bodies before any state change.
	if len(bytes.TrimSpace(b)) > 0 && bytes.TrimSpace(b)[0] == '{' {
		d := json.NewDecoder(bytes.NewReader(b))
		if _, e = d.Token(); e != nil {
			return ErrInput
		}
		seen := map[string]bool{}
		for d.More() {
			t, e := d.Token()
			if e != nil {
				return ErrInput
			}
			k, ok := t.(string)
			if !ok || seen[k] {
				return ErrInput
			}
			seen[k] = true
			var v json.RawMessage
			if d.Decode(&v) != nil {
				return ErrInput
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrInput
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInput
	}
	return nil
}
func peer(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return "unknown"
}
func (s *Service) Handler() http.Handler { m := http.NewServeMux(); s.RegisterRoutes(m); return m }

// RegisterRoutes permits the host to combine the owned first-party flow with
// authority.Handler's existing token, refresh, current-account and session APIs.
func (s *Service) RegisterRoutes(m *http.ServeMux) {
	m.Handle("/stargate/contacts", s.ContactHandler())
	m.Handle("/stargate/contacts/", s.ContactHandler())
	m.HandleFunc("POST /stargate/accounts", func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		if e := decode(w, r, &req); e != nil {
			failure(w, e)
			return
		}
		a, e := s.Register(r.Context(), req, peer(r))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 202, a)
	})
	m.HandleFunc("POST /stargate/accounts/verify", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		e := decode(w, r, &req)
		if e == nil {
			e = s.limit(r.Context(), "verify-email-peer", peer(r), 60, 10*time.Minute)
		}
		if e == nil {
			e = s.VerifyEmail(r.Context(), req.Code)
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	recovery := func(purpose string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Account string `json:"account"`
				Captcha string `json:"captcha_token"`
			}
			e := decode(w, r, &req)
			if e == nil {
				if purpose == "reset_password" {
					e = s.RequestReset(r.Context(), req.Account, req.Captcha, peer(r))
				} else {
					e = s.ResendVerification(r.Context(), req.Account, req.Captcha, peer(r))
				}
			}
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 202, map[string]string{"status": "accepted"})
		}
	}
	m.HandleFunc("POST /stargate/accounts/verification", recovery("verify_email"))
	m.HandleFunc("POST /passport/accounts/recovery/password", recovery("reset_password"))
	m.HandleFunc("PATCH /passport/accounts/recovery/password", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code     string `json:"code"`
			Password string `json:"password"`
		}
		e := decode(w, r, &req)
		if e == nil {
			e = s.limit(r.Context(), "reset-complete-peer", peer(r), 20, 10*time.Minute)
		}
		if e == nil {
			e = s.ResetPassword(r.Context(), req.Code, req.Password)
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	m.HandleFunc("POST /stargate/auth/challenge", func(w http.ResponseWriter, r *http.Request) {
		var req StartRequest
		if e := decode(w, r, &req); e != nil {
			failure(w, e)
			return
		}
		req.UserAgent = r.UserAgent()
		if len(req.UserAgent) > 512 {
			req.UserAgent = req.UserAgent[:512]
		}
		v, e := s.Start(r.Context(), req, peer(r))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, v)
	})
	m.HandleFunc("GET /stargate/auth/challenge/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Challenge(r.Context(), r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, v)
	})
	m.HandleFunc("GET /stargate/auth/challenge/{id}/factors", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ChallengeFactors(r.Context(), r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, v)
	})
	m.HandleFunc("POST /stargate/auth/challenge/{id}/factors/{factor}", func(w http.ResponseWriter, r *http.Request) {
		if e := s.PrepareFactor(r.Context(), r.PathValue("id"), r.PathValue("factor")); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	m.HandleFunc("PATCH /stargate/auth/challenge/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FactorID string `json:"factor_id"`
			Password string `json:"password"`
		}
		if e := decode(w, r, &req); e != nil {
			failure(w, e)
			return
		}
		v, e := s.VerifyFactor(r.Context(), r.PathValue("id"), req.FactorID, req.Password, peer(r))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, v)
	})
	m.HandleFunc("GET /stargate/factors", func(w http.ResponseWriter, r *http.Request) {
		token, e := authority.BearerToken(r)
		if e != nil {
			failure(w, e)
			return
		}
		v, e := s.Factors(r.Context(), token)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, v)
	})
	m.HandleFunc("POST /stargate/factors", func(w http.ResponseWriter, r *http.Request) {
		token, e := authority.BearerToken(r)
		if e != nil {
			failure(w, e)
			return
		}
		var req struct {
			Type   int     `json:"type"`
			Secret *string `json:"secret"`
		}
		if e = decode(w, r, &req); e != nil {
			failure(w, e)
			return
		}
		secret := ""
		if req.Secret != nil {
			secret = *req.Secret
		}
		v, e := s.CreateFactor(r.Context(), token, req.Type, secret)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 201, v)
	})
	m.HandleFunc("POST /stargate/factors/{id}/enable", func(w http.ResponseWriter, r *http.Request) {
		token, e := authority.BearerToken(r)
		if e != nil {
			failure(w, e)
			return
		}
		var code string
		if e = decode(w, r, &code); e != nil {
			failure(w, e)
			return
		}
		v, e := s.EnableFactor(r.Context(), token, r.PathValue("id"), code)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, v)
	})
	m.HandleFunc("DELETE /stargate/factors/{id}", func(w http.ResponseWriter, r *http.Request) {
		token, e := authority.BearerToken(r)
		if e == nil {
			e = s.DeleteFactor(r.Context(), token, r.PathValue("id"))
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	m.HandleFunc("POST /stargate/factors/{id}/disable", func(w http.ResponseWriter, r *http.Request) {
		token, e := authority.BearerToken(r)
		if e == nil {
			e = s.DisableFactor(r.Context(), token, r.PathValue("id"))
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	m.HandleFunc("PUT /stargate/accounts/me/password", func(w http.ResponseWriter, r *http.Request) {
		token, e := authority.BearerToken(r)
		if e != nil {
			failure(w, e)
			return
		}
		var req struct {
			Old string `json:"old_password"`
			New string `json:"new_password"`
		}
		e = decode(w, r, &req)
		if e == nil {
			e = s.ChangePassword(r.Context(), token, req.Old, req.New)
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
}
