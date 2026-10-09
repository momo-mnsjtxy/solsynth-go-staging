// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from DysonNetwork Develop at
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.
package develop

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

type packageHandler struct {
	service   *PackageService
	candidate bool
}

func NewPackageHandler(service *PackageService) http.Handler { return packageHandler{service: service} }
func NewSyntheticCompatibilityPackageHandler(service *PackageService) http.Handler {
	return packageHandler{service: service, candidate: true}
}

func (h packageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Error string `json:"error"`
		}{code})
	}
	// These three values are explicitly assigned by the licensed controller.
	// Additional shared ApiError members/serialization remain unverified.
	notFound := func(code, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Status  int    `json:"status"`
		}{code, message, http.StatusNotFound})
	}
	if !h.candidate {
		fail(http.StatusServiceUnavailable, "packages_policy_unverified")
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "api" || parts[2] != "miniapps" || parts[3] == "" || parts[4] != "package" {
		fail(http.StatusNotFound, "packages_route_not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(http.StatusMethodNotAllowed, "packages_method_not_allowed")
		return
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "X-Test-Account") {
			fail(http.StatusForbidden, "packages_identity_unsupported")
			return
		}
	}
	if ValidatePackageSlug(parts[3]) != nil {
		fail(http.StatusBadRequest, "packages_slug_invalid")
		return
	}
	result, err := h.service.Download(r.Context(), parts[3])
	if err != nil {
		switch {
		case errors.Is(err, ErrPackageNotFound):
			notFound("DEV_MINI_APP_NOT_FOUND", "Mini app not found")
		case errors.Is(err, ErrPackageMissing):
			notFound("DEV_MINI_APP_NO_PACKAGE", "Mini app has no published package")
		default:
			fail(http.StatusServiceUnavailable, "packages_unavailable")
		}
		return
	}
	// Each GET changes the counter. Candidate cache policy avoids serving a
	// cached transfer in place of a new request; upstream cache policy is unknown.
	w.Header().Set("Cache-Control", "no-store")
	if result.RedirectURL != "" {
		w.Header().Set("Location", result.RedirectURL)
		w.WriteHeader(http.StatusFound)
		return
	}
	filename := result.Slug + ".zip"
	// Do not copy database-supplied path/control characters into a filename.
	if ValidatePackageSlug(result.Slug) != nil {
		fail(http.StatusServiceUnavailable, "packages_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Data)
}
