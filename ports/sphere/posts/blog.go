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
	"unicode/utf8"
)

type LinkEmbed struct {
	Type string `json:"type"`
	reader.Link
}
type Metadata struct {
	Embeds []LinkEmbed `json:"embeds"`
}

func BlogMetadata(link reader.Link) *Metadata {
	return &Metadata{Embeds: []LinkEmbed{{Type: "link", Link: link}}}
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
