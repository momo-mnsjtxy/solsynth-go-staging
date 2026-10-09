// SPDX-License-Identifier: AGPL-3.0-only
// Modified 2026-10-07: source-derived Go port of the licensed DysonNetwork
// BadgeDiscovery controller at 4ee2d0cebb9da8284a92483fee6dce795bf6947c.
package badgediscovery

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

const manifestPath = "/.well-known/badges"
const iconPrefix = manifestPath + "/icons/"

type Series struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Order      int    `json:"order"`
}
type Badge struct {
	Identifier            string  `json:"identifier"`
	AchievementIdentifier string  `json:"achievement_identifier"`
	Label                 string  `json:"label"`
	Caption               string  `json:"caption"`
	Icon                  string  `json:"icon"`
	Color                 string  `json:"color"`
	IconURL               *string `json:"icon_url"`
	LocalizationKey       string  `json:"localization_key"`
	Category              string  `json:"category"`
	Series                *Series `json:"series"`
	Hidden                bool    `json:"hidden"`
}
type Manifest struct {
	Version int     `json:"version"`
	Badges  []Badge `json:"badges"`
}
type Config struct {
	// IconsPath retains upstream semantics: blank or nonexistent directory
	// leaves manifest icon_url null. No implicit fallback hides missing config.
	IconsPath string
	// TraceID supplies the host request trace. Nil uses a process-local sequence.
	TraceID func(*http.Request) string
}

//go:embed manifest.json
var catalogJSON []byte

// Catalog returns a fresh manifest. Directory existence controls all URLs, even
// if an individual SVG is absent; this matches the licensed controller.
func Catalog(hasIcons bool) Manifest {
	var entries []Badge
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		panic("invalid embedded badge catalog")
	}
	if hasIcons {
		for i := range entries {
			url := iconPrefix + entries[i].Icon
			entries[i].IconURL = &url
		}
	}
	return Manifest{Version: 1, Badges: entries}
}

// RouteMatches lets a host route this public family without changing unrelated
// endpoints. Actual method and basename checks remain in the handler.
func RouteMatches(path string) bool {
	path = strings.TrimSuffix(path, "/")
	return equalRouteLiteral(path, manifestPath) || iconRoute(path)
}

func iconRoute(path string) bool {
	return len(path) > len(iconPrefix) &&
		equalRouteLiteral(path[:len(iconPrefix)], iconPrefix) &&
		!strings.Contains(path[len(iconPrefix):], "/")
}

type handler struct{ config Config }

func NewHandler(config Config) http.Handler { return &handler{config: config} }

var traces atomic.Uint64

func (h *handler) trace(r *http.Request) string {
	if h.config.TraceID != nil {
		return h.config.TraceID(r)
	}
	return "badges-" + strconv.FormatUint(traces.Add(1), 16)
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
	TraceID string `json:"traceId"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (h *handler) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message, Status: status, TraceID: h.trace(r)})
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	isManifest := equalRouteLiteral(path, manifestPath)
	isIcon := iconRoute(path)
	if !isManifest && !isIcon {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if isManifest {
		w.Header().Set("Cache-Control", "public,max-age=3600")
		hasIcons := false
		if strings.TrimSpace(h.config.IconsPath) != "" {
			info, err := os.Stat(h.config.IconsPath)
			hasIcons = err == nil && info.IsDir()
		}
		writeJSON(w, http.StatusOK, Catalog(hasIcons))
		return
	}
	w.Header().Set("Cache-Control", "public,max-age=86400")
	h.icon(w, r, path[len(iconPrefix):])
}
func (h *handler) icon(w http.ResponseWriter, r *http.Request, name string) {
	notFound := func() { h.fail(w, r, http.StatusNotFound, "PASSPORT_BADGE_ICON_NOT_FOUND", "Badge icon not found.") }
	if strings.TrimSpace(h.config.IconsPath) == "" {
		notFound()
		return
	}
	// Deliberate confinement hardening: the original string-prefix filesystem
	// check is not reproduced. Keep names single-segment and use os.Root so
	// symlink resolution cannot escape the configured root.
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		h.fail(w, r, http.StatusBadRequest, "PASSPORT_BADGE_ICON_INVALID_PATH", "Invalid icon path.")
		return
	}
	root, err := os.OpenRoot(h.config.IconsPath)
	if err != nil {
		notFound()
		return
	}
	defer root.Close()
	f, err := root.Open(name + ".svg")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			notFound()
			return
		}
		h.fail(w, r, http.StatusBadRequest, "PASSPORT_BADGE_ICON_INVALID_PATH", "Invalid icon path.")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		notFound()
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "SERVER_ERROR", "An internal server error occurred.")
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// Route literals are ASCII. Unicode simple-fold equivalences such as long s
// and Kelvin sign are not equivalent to ASCII letters in the retained MVC oracle.
func equalRouteLiteral(value, literal string) bool {
	if len(value) != len(literal) {
		return false
	}
	for i := range len(literal) {
		a, b := value[i], literal[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
