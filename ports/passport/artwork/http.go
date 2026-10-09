// SPDX-License-Identifier: AGPL-3.0-only
package artwork

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

type Service struct {
	Repository Repository
	Authority  media.Authority
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func fail(w http.ResponseWriter, err error) {
	err = media.Normalize(err)
	status := 503
	var m *media.Failure
	var a *authority.Failure
	if errors.As(err, &m) {
		status = m.Status
	}
	if errors.As(err, &a) {
		status = a.Status
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="passport"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(err)
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil || s.Authority == nil {
		fail(w, media.ErrUnavailable)
		return
	}
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != r.URL.Path || r.URL.User != nil || len(r.URL.Query()) != 0 {
		fail(w, media.ErrInvalid)
		return
	}
	present := false
	for key := range r.Header {
		k := strings.ToLower(key)
		if k == "authorization" {
			present = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-synthetic-") || strings.HasPrefix(k, "x-test-") {
			fail(w, media.ErrForbidden)
			return
		}
	}
	if p, ok := s.Authority.(interface{ RequestIdentityPresent(*http.Request) bool }); ok {
		present = present || p.RequestIdentityPresent(r)
	}
	p, err := s.Authority.AuthenticateRequest(r)
	if err != nil && (present || !errors.Is(err, authority.ErrInvalid)) {
		fail(w, err)
		return
	}
	actor := ""
	if err == nil {
		p, err = s.Authority.AuthenticatePrincipal(r.Context(), p)
		if err != nil {
			fail(w, err)
			return
		}
		actor = p.Account.ID
	}
	const root = "/api/presence/artworks"
	if r.URL.Path == root && r.Method == http.MethodPost {
		if actor == "" {
			fail(w, authority.ErrInvalid)
			return
		}
		if _, err = s.Authority.AuthorizePrincipal(r.Context(), p, ManagePermission, media.CreatePermission); err != nil {
			fail(w, err)
			return
		}
		// Bound the entire multipart body, including headers and non-file fields.
		r.Body = http.MaxBytesReader(w, r.Body, MaxBytes)
		parts, e := r.MultipartReader()
		if e != nil {
			fail(w, media.ErrInvalid)
			return
		}
		var data []byte
		typ := ""
		found := false
		for {
			part, e := parts.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				fail(w, media.ErrInvalid)
				return
			}
			if found || !strings.EqualFold(part.FormName(), "file") || part.FileName() == "" {
				part.Close()
				fail(w, media.ErrInvalid)
				return
			}
			found = true
			typ, e = ImageType(part.Header.Get("Content-Type"))
			if e != nil {
				part.Close()
				fail(w, e)
				return
			}
			data, e = io.ReadAll(part)
			part.Close()
			if e != nil {
				var large *http.MaxBytesError
				if errors.As(e, &large) {
					fail(w, media.ErrTooLarge)
				} else {
					fail(w, media.ErrInvalid)
				}
				return
			}
		}
		if !found || len(data) == 0 {
			fail(w, media.ErrInvalid)
			return
		}
		a, e := s.Repository.UploadArtwork(r.Context(), actor, typ, data)
		if e != nil {
			fail(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(a)
		return
	}
	if strings.HasPrefix(r.URL.Path, root+"/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		hash, e := NormalizeHash(strings.TrimPrefix(r.URL.Path, root+"/"))
		if e != nil {
			fail(w, e)
			return
		}
		a, body, e := s.Repository.OpenArtwork(r.Context(), actor, hash)
		if e != nil {
			fail(w, e)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", a.MIMEType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "artwork", time.Time{}, body)
		return
	}
	if r.URL.Path == root || strings.HasPrefix(r.URL.Path, root+"/") {
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(405)
		return
	}
	fail(w, media.ErrNotFound)
}
