// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"solsynth.local/go-migration/ports/authority"
)

type Service struct {
	Repository   Repository
	Authority    Authority
	MaxFileBytes int64
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	err = Normalize(err)
	var f *Failure
	var a *authority.Failure
	if errors.As(err, &f) {
		reply(w, f.Status, f)
		return
	}
	if errors.As(err, &a) {
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="media"`)
		}
		reply(w, a.Status, a)
		return
	}
	reply(w, 503, ErrUnavailable)
}
func (s *Service) principal(r *http.Request, permission string) (authority.Principal, error) {
	if s.Authority == nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		return p, e
	}
	if permission == "" {
		return s.Authority.AuthenticatePrincipal(r.Context(), p)
	}
	return s.Authority.AuthorizePrincipal(r.Context(), p, permission)
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w = headWriter{w}
	}
	if s == nil || s.Repository == nil || s.Authority == nil || s.MaxFileBytes <= 0 {
		failure(w, ErrUnavailable)
		return
	}
	p := r.URL.Path
	if p == "" || path.Clean(p) != p || strings.Contains(p, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != p || r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	authPresent := false
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			authPresent = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-synthetic-") || strings.HasPrefix(k, "x-test-") {
			failure(w, ErrForbidden)
			return
		}
	}
	for k := range r.URL.Query() {
		if k == "access_token" || k == "token" || k == "tk" {
			failure(w, ErrInvalid)
			return
		}
	}
	actor := ""
	principal, err := s.principal(r, "")
	if err == nil {
		actor = principal.Account.ID
	} else if authPresent || !errors.Is(err, authority.ErrInvalid) {
		failure(w, err)
		return
	}
	// Canonical host removes Authorization after binding opaque proof. Request
	// writers still require proof; an anonymous request can only read public files.
	if p == "/api/files/upload/direct" && r.Method == http.MethodPost {
		s.direct(w, r)
		return
	}
	if s.resumableHTTP(w, r) {
		return
	}
	if p == "/api/media/files" && r.Method == http.MethodPost {
		principal, err = s.principal(r, CreatePermission)
		if err != nil {
			failure(w, err)
			return
		}
		if len(r.URL.Query()) != 0 || r.ContentLength < 0 {
			failure(w, ErrInvalid)
			return
		}
		if r.ContentLength > s.MaxFileBytes {
			failure(w, ErrTooLarge)
			return
		}
		public := false
		if v := r.Header.Get("X-File-Public"); v != "" {
			if v != "true" && v != "false" {
				failure(w, ErrInvalid)
				return
			}
			public = v == "true"
		}
		for _, name := range []string{"Content-Type", "X-File-Name", "X-Content-SHA256", "Idempotency-Key", "X-File-Public"} {
			if len(r.Header.Values(name)) > 1 {
				failure(w, ErrInvalid)
				return
			}
		}
		u := Upload{Name: r.Header.Get("X-File-Name"), MIMEType: r.Header.Get("Content-Type"), SHA256: r.Header.Get("X-Content-SHA256"), IdempotencyKey: r.Header.Get("Idempotency-Key"), Size: r.ContentLength, Public: public}
		r.Body = http.MaxBytesReader(w, r.Body, s.MaxFileBytes+1)
		f, replayed, e := s.Repository.Upload(r.Context(), principal.Account.ID, u, r.Body)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("Location", "/api/media/files/"+f.ID+"/info")
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
			w.Header().Set("Idempotency-Replayed", "true")
		}
		reply(w, status, f)
		return
	}
	if p == "/api/media/quota" && r.Method == http.MethodGet {
		principal, err = s.principal(r, ReadPermission)
		if err != nil {
			failure(w, err)
			return
		}
		q, e := s.Repository.Quota(r.Context(), principal.Account.ID)
		if e != nil {
			failure(w, e)
			return
		}
		reply(w, 200, q)
		return
	}
	prefix := "/api/media/files/"
	legacy := false
	if strings.HasPrefix(p, "/api/files/") {
		prefix = "/api/files/"
		legacy = true
	}
	if !strings.HasPrefix(p, prefix) {
		failure(w, ErrNotFound)
		return
	}
	rest := strings.TrimPrefix(p, prefix)
	id, suffix, _ := strings.Cut(rest, "/")
	if legacy && (id == "upload" || id == "root" || id == "me" || id == "batches" || id == "folders" || id == "recycle" || id == "unindexed") {
		failure(w, ErrUnsupported)
		return
	}
	if legacy {
		id = legacyID(id)
	}
	if !authority.ValidID(id) {
		failure(w, ErrNotFound)
		return
	}
	if suffix != "" && suffix != "info" {
		failure(w, ErrUnsupported)
		return
	}
	if r.Method == http.MethodDelete && suffix == "" {
		principal, err = s.principal(r, DeletePermission)
		if err != nil {
			failure(w, err)
			return
		}
		f, e := s.Repository.Delete(r.Context(), principal.Account.ID, id)
		if e != nil {
			failure(w, e)
			return
		}
		if legacy {
			reply(w, 200, legacyFile(f))
		} else {
			reply(w, 200, f)
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, DELETE")
		reply(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
		return
	}
	for k, vs := range r.URL.Query() {
		if len(vs) != 1 || k != "original" && k != "download" || vs[0] != "true" && vs[0] != "false" {
			failure(w, ErrUnsupported)
			return
		}
	}
	if suffix == "info" {
		f, e := s.Repository.Info(r.Context(), actor, id)
		if e != nil {
			failure(w, e)
			return
		}
		if legacy {
			reply(w, 200, legacyFile(f))
		} else {
			reply(w, 200, f)
		}
		return
	}
	f, body, e := s.Repository.Open(r.Context(), actor, id)
	if e != nil {
		failure(w, e)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", f.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("ETag", `"sha256-`+f.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// Untrusted active content is always downloaded; it is never served as an
	// inline application document on the authenticated backend origin.
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	http.ServeContent(w, r, f.Name, f.UpdatedAt, body)
}

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(b []byte) (int, error) { return len(b), nil }
