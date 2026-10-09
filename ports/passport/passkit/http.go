// SPDX-License-Identifier: AGPL-3.0-only
package passkit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"

	"solsynth.local/go-migration/ports/authority"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Logger     *slog.Logger
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func ValidOpaque(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 || c == '/' || c == '\\' {
			return false
		}
	}
	return true
}
func fail(w http.ResponseWriter, e error) {
	e = Normalize(e)
	status := 503
	var p *Failure
	var a *authority.Failure
	if errors.As(e, &p) {
		status = p.Status
	}
	if errors.As(e, &a) {
		status = a.Status
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `ApplePass realm="passkit"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}
func token(r *http.Request) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", ErrUnauthorized
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "ApplePass") || len(parts[1]) != 48 {
		return "", ErrUnauthorized
	}
	return parts[1], nil
}
func object(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	dec := json.NewDecoder(r.Body)
	first, e := dec.Token()
	if e != nil || first != json.Delim('{') {
		return nil, ErrInvalid
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		key, e := dec.Token()
		if e != nil {
			return nil, ErrInvalid
		}
		name, ok := key.(string)
		if !ok {
			return nil, ErrInvalid
		}
		if _, ok = out[name]; ok {
			return nil, ErrInvalid
		}
		var value json.RawMessage
		if e = dec.Decode(&value); e != nil {
			return nil, ErrInvalid
		}
		out[name] = value
	}
	if _, e = dec.Token(); e != nil {
		return nil, ErrInvalid
	}
	var trailing any
	if e = dec.Decode(&trailing); e != io.EOF {
		return nil, ErrInvalid
	}
	return out, nil
}
func sendBundle(w http.ResponseWriter, r *http.Request, b Bundle, filename string) {
	w.Header().Set("Content-Type", "application/vnd.apple.pkpass")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	etag := `W/"` + b.Tag + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", b.UpdatedAt.UTC().Format(http.TimeFormat))
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b.Data)))
	w.WriteHeader(200)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b.Data)
	}
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != r.URL.Path || r.URL.User != nil {
		fail(w, ErrInvalid)
		return
	}
	for key := range r.Header {
		v := strings.ToLower(key)
		if v == "x-account-id" || v == "x-user-id" || v == "x-role" || v == "proxy-authorization" || strings.HasPrefix(v, "x-test-") || strings.HasPrefix(v, "x-synthetic-") {
			fail(w, authority.ErrForbidden)
			return
		}
	}
	if s == nil || s.Repository == nil {
		fail(w, ErrUnavailable)
		return
	}
	const member = "/api/accounts/me/passbook/member"
	if r.URL.Path == member {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(405)
			return
		}
		if len(r.URL.Query()) != 0 {
			fail(w, ErrInvalid)
			return
		}
		if s.Authority == nil {
			fail(w, ErrUnavailable)
			return
		}
		p, e := s.Authority.AuthenticateRequest(r)
		if e == nil {
			p, e = s.Authority.AuthorizePrincipal(r.Context(), p, ProvisionPermission)
		}
		if e == nil {
			e = RequireProvisioningPrincipal(p)
		}
		if e != nil {
			fail(w, e)
			return
		}
		b, e := s.Repository.Member(r.Context(), p.Account.ID)
		if e != nil {
			fail(w, e)
			return
		}
		sendBundle(w, r, b, "solian-member.pkpass")
		return
	}
	const root = "/api/passkit/v1"
	if r.URL.Path == root && r.Method == "GET" {
		if len(r.URL.Query()) != 0 {
			fail(w, ErrInvalid)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"service": "passkit", "version": "v1", "message": "Apple Wallet PassKit service endpoint.", "endpoints": []string{"POST devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}/{serialNumber}", "DELETE devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}/{serialNumber}", "GET devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}", "GET passes/{passTypeIdentifier}/{serialNumber}", "POST log"}})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, root+"/"), "/")
	if !strings.HasPrefix(r.URL.Path, root+"/") {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "log" && r.Method == "POST" {
		if len(r.URL.Query()) != 0 {
			fail(w, ErrInvalid)
			return
		}
		body, e := object(w, r)
		if e != nil || len(body) != 1 {
			fail(w, ErrInvalid)
			return
		}
		var logs []string
		if e = json.Unmarshal(body["logs"], &logs); e != nil || len(logs) > 50 {
			fail(w, ErrInvalid)
			return
		}
		for _, line := range logs {
			if len(line) > 1024 {
				fail(w, ErrInvalid)
				return
			}
		}
		logger := s.Logger
		if logger == nil {
			logger = slog.Default()
		}
		digest := sha256.Sum256(body["logs"])
		logger.InfoContext(r.Context(), "Apple Wallet diagnostics received", "entries", len(logs), "content_sha256", hex.EncodeToString(digest[:]))
		w.WriteHeader(200)
		return
	}
	// Apple's serial-list protocol authenticates possession of the device
	// library identifier. A supplied ApplePass token narrows that list; an
	// invalid supplied header never downgrades to device-only authentication.
	deviceQuery := len(parts) == 4 && parts[0] == "devices" && parts[2] == "registrations" && r.Method == "GET"
	auth := ""
	var e error
	if len(r.Header.Values("Authorization")) != 0 || !deviceQuery {
		auth, e = token(r)
		if e != nil {
			fail(w, e)
			return
		}
	}
	if len(parts) == 3 && parts[0] == "passes" && (r.Method == "GET" || r.Method == "HEAD") {
		if len(r.URL.Query()) != 0 {
			fail(w, ErrInvalid)
			return
		}
		b, e := s.Repository.Latest(r.Context(), parts[1], parts[2], auth)
		if e != nil {
			fail(w, e)
			return
		}
		sendBundle(w, r, b, "member.pkpass")
		return
	}
	if (len(parts) == 4 || len(parts) == 5) && parts[0] == "devices" && parts[2] == "registrations" {
		device, typ := parts[1], parts[3]
		if !ValidOpaque(device, 255) || !ValidOpaque(typ, 255) {
			fail(w, ErrInvalid)
			return
		}
		if len(parts) == 4 && r.Method == "GET" {
			q := r.URL.Query()
			if len(q) > 1 || len(q["passesUpdatedSince"]) > 1 || len(q) == 1 && q.Get("passesUpdatedSince") == "" {
				fail(w, ErrInvalid)
				return
			}
			v, e := s.Repository.Updates(r.Context(), typ, device, auth, q.Get("passesUpdatedSince"))
			if e != nil {
				fail(w, e)
				return
			}
			if len(v.SerialNumbers) == 0 {
				w.WriteHeader(204)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(v)
			return
		}
		if len(parts) == 5 && len(r.URL.Query()) == 0 {
			serial := parts[4]
			if r.Method == "DELETE" {
				if e = s.Repository.Unregister(r.Context(), typ, serial, auth, device); e != nil {
					fail(w, e)
					return
				}
				w.WriteHeader(200)
				return
			}
			if r.Method == "POST" {
				body, e := object(w, r)
				if e != nil || len(body) != 1 {
					fail(w, ErrInvalid)
					return
				}
				value, ok := body["pushToken"]
				if !ok {
					value = body["push_token"]
				}
				var push string
				if e = json.Unmarshal(value, &push); e != nil || !ValidOpaque(push, 512) {
					fail(w, ErrInvalid)
					return
				}
				created, e := s.Repository.Register(r.Context(), typ, serial, auth, device, push)
				if e != nil {
					fail(w, e)
					return
				}
				if created {
					w.WriteHeader(201)
				} else {
					w.WriteHeader(200)
				}
				return
			}
		}
	}
	http.NotFound(w, r)
}
