// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"unicode/utf8"
)

type LinkEmbed struct {
	Type string `json:"type"`
	reader.Link
}
type Metadata struct {
	Composed  []Embed     `json:"-"`
	Thumbnail *string     `json:"thumbnail,omitempty"`
	Embeds    []LinkEmbed `json:"embeds"`
}

func BlogMetadata(link reader.Link) *Metadata {
	return &Metadata{Embeds: []LinkEmbed{{Type: "link", Link: link}}}
}

// IsNativeSolianLink identifies Solian pages rendered directly by the client.
// It only controls preview presentation; publish permission and verified-domain
// checks still apply to blog posts using these URLs.
func IsNativeSolianLink(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Opaque != "" || !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return host == "solian.app" || strings.HasSuffix(host, ".solian.app")
}

type BlogPermissionRepository interface {
	CheckBlogPermission(context.Context, string, string, string) (blogs.Permission, error)
}

func registerBlog(m *http.ServeMux, repo Repository, identity func(http.ResponseWriter, *http.Request, string) (string, bool)) {
	m.HandleFunc("POST /api/posts/blog/check-permission", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, "")
		if !ok {
			return
		}
		if actor == "" {
			failure(w, ErrUnauthenticated)
			return
		}
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil {
			failure(w, ErrInvalid)
			return
		}
		if e = query(q, "pub"); e != nil {
			failure(w, e)
			return
		}
		ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || ct != "application/json" {
			failure(w, ErrInvalid)
			return
		}
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if e != nil || !utf8.Valid(b) {
			failure(w, ErrInvalid)
			return
		}
		d := json.NewDecoder(bytes.NewReader(b))
		t, e := d.Token()
		if e != nil || t != json.Delim('{') {
			failure(w, ErrInvalid)
			return
		}
		var raw string
		seen := false
		for d.More() {
			k, e := d.Token()
			if e != nil || k != "url" || seen || d.Decode(&raw) != nil {
				failure(w, ErrInvalid)
				return
			}
			seen = true
		}
		if _, e = d.Token(); e != nil {
			failure(w, ErrInvalid)
			return
		}
		if _, e = d.Token(); e != io.EOF || !seen {
			failure(w, ErrInvalid)
			return
		}
		rp, ok := repo.(BlogPermissionRepository)
		if !ok {
			failure(w, ErrUnsupported)
			return
		}
		out, e := rp.CheckBlogPermission(r.Context(), actor, q.Get("pub"), raw)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
}
