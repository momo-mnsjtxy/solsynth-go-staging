package badgediscovery

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func request(h http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}
func TestCatalogCompleteAndDetached(t *testing.T) {
	m := Catalog(false)
	if m.Version != 1 || len(m.Badges) != 23 {
		t.Fatal(m)
	}
	if m.Badges[0].Identifier != "progression.post.expert" || m.Badges[22].Identifier != "progression.hidden.social_butterfly_day" {
		t.Fatal("catalog order")
	}
	seen := map[string]bool{}
	hidden := 0
	for _, b := range m.Badges {
		if seen[b.Identifier] || b.IconURL != nil {
			t.Fatal(b)
		}
		seen[b.Identifier] = true
		if b.Hidden {
			hidden++
		}
	}
	if hidden != 5 {
		t.Fatal(hidden)
	}
	m.Badges[1].Series.Title = "mutated"
	if Catalog(false).Badges[1].Series.Title != "Featured Posts" {
		t.Fatal("catalog alias")
	}
	with := Catalog(true)
	for _, b := range with.Badges {
		if b.IconURL == nil || *b.IconURL != iconPrefix+b.Icon {
			t.Fatal(b)
		}
	}
}
func TestManifestDirectoryConfiguration(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, path string
		has        bool
	}{{"unset", "", false}, {"blank", " \t", false}, {"missing", filepath.Join(dir, "absent"), false}, {"existing-empty", dir, true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(NewHandler(Config{IconsPath: tc.path}), "GET", manifestPath)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "public,max-age=3600" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatal(w.Code, w.Header())
			}
			var got Manifest
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Badges) != 23 || (got.Badges[0].IconURL != nil) != tc.has {
				t.Fatal(got)
			}
			if !bytes.Contains(w.Body.Bytes(), []byte(`"series":null`)) {
				t.Fatal("series null omitted")
			}
			if !tc.has && !bytes.Contains(w.Body.Bytes(), []byte(`"icon_url":null`)) {
				t.Fatal("icon_url null omitted")
			}
		})
	}
}
func TestRouteAndMethodContract(t *testing.T) {
	h := NewHandler(Config{})
	for _, path := range []string{manifestPath, manifestPath + "/", "/.WELL-KNOWN/BADGES"} {
		if !RouteMatches(path) {
			t.Fatal(path)
		}
		if w := request(h, "GET", path); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	for _, method := range []string{"HEAD", "POST", "PUT", "OPTIONS"} {
		w := request(h, method, manifestPath)
		if w.Code != 405 || w.Header().Get("Allow") != "GET" || w.Header().Get("Cache-Control") != "" || w.Body.Len() != 0 {
			t.Fatal(method, w.Code, w.Header(), w.Body.String())
		}
	}
	if RouteMatches("/unrelated") || request(h, "GET", "/unrelated").Code != 404 {
		t.Fatal("unknown route")
	}
}
func TestIconBytesHeadersAndConditionals(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0L1 1"/></svg>`)
	if err := os.WriteFile(filepath.Join(dir, "custom.svg"), data, 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Config{IconsPath: dir})
	for _, header := range []struct{ key, value string }{{"Range", "bytes=0-3"}, {"Range", "bytes=999999-"}, {"If-None-Match", "*"}, {"If-Modified-Since", "Wed, 21 Oct 2099 07:28:00 GMT"}} {
		r := httptest.NewRequest("GET", iconPrefix+"custom", nil)
		r.Header.Set(header.key, header.value)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Content-Type") != "image/svg+xml" || w.Header().Get("Cache-Control") != "public,max-age=86400" {
			t.Fatal(w.Code, w.Header())
		}
		for _, name := range []string{"ETag", "Last-Modified", "Accept-Ranges", "Content-Range"} {
			if w.Header().Get(name) != "" {
				t.Fatal(name)
			}
		}
	}
}
func TestErrorEnvelopeAndCache(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(Config{IconsPath: dir, TraceID: func(*http.Request) string { return "fixed-trace" }})
	w := request(h, "GET", iconPrefix+"absent")
	var e map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if w.Code != 404 || e["code"] != "PASSPORT_BADGE_ICON_NOT_FOUND" || e["traceId"] != "fixed-trace" || e["status"] != float64(404) || len(e) != 4 {
		t.Fatal(w.Code, e)
	}
	if w.Header().Get("Cache-Control") != "public,max-age=86400" {
		t.Fatal(w.Header())
	}
	if request(NewHandler(Config{}), "GET", iconPrefix+"absent").Code != 404 {
		t.Fatal("unconfigured root")
	}
}
func TestRootConfinement(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.svg")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.svg")); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Config{IconsPath: dir})
	w := request(h, "GET", iconPrefix+"linked")
	if w.Code != 400 || strings.Contains(w.Body.String(), "outside") {
		t.Fatal(w.Code, w.Body.String())
	}
	if request(h, "GET", iconPrefix+"nested/name").Code != 404 {
		t.Fatal("multisegment path incorrectly matched an action")
	}
}

func TestExplicitEmptyTraceIsRetained(t *testing.T) {
	h := NewHandler(Config{TraceID: func(*http.Request) string { return "" }})
	w := request(h, "GET", iconPrefix+"absent")
	if !bytes.Contains(w.Body.Bytes(), []byte(`"traceId":""`)) {
		t.Fatal("explicit empty trace omitted", w.Body.String())
	}
}
func TestAllManifestAssetsArePresentAndSVG(t *testing.T) {
	h := NewHandler(Config{IconsPath: "assets"})
	seen := map[string]bool{}
	for _, b := range Catalog(true).Badges {
		if seen[b.Icon] {
			continue
		}
		seen[b.Icon] = true
		w := request(h, "GET", *b.IconURL)
		if w.Code != 200 {
			t.Fatal(b.Icon, w.Code)
		}
		decoder := xml.NewDecoder(bytes.NewReader(w.Body.Bytes()))
		found := false
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(b.Icon, err)
			}
			if start, ok := token.(xml.StartElement); ok && start.Name.Local == "svg" {
				found = true
			}
		}
		if !found {
			t.Fatal(b.Icon, "not SVG")
		}
	}
	if len(seen) != 12 {
		t.Fatal("manifest icon count", len(seen))
	}
	// repeat.svg is preserved as a known licensed resource even though the
	// current manifest does not reference it.
	if w := request(h, "GET", iconPrefix+"repeat"); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
