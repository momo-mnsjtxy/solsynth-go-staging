// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	e = NormalizeError(e)
	status := 503
	var p *Failure
	var a *authority.Failure
	var c *accounts.Failure
	switch {
	case errors.As(e, &p):
		status = p.Status
	case errors.As(e, &a):
		status = a.Status
	case errors.As(e, &c):
		status = c.Status
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="posts"`)
	}
	respond(w, status, e)
}

// Decode explicitly recognizes the observed composer envelope. Neutral values
// for unsupported fields are accepted; meaningful values fail instead of being
// silently discarded. Duplicate keys and identity/publisher body fields fail.
func decode(w http.ResponseWriter, r *http.Request) (Patch, error) {
	var p Patch
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return p, &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if e != nil || !utf8.Valid(body) {
		return p, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return p, ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return p, ErrInvalid
		}
		k, ok := t.(string)
		if !ok || seen[k] {
			return p, ErrInvalid
		}
		seen[k] = true
		var b json.RawMessage
		if d.Decode(&b) != nil {
			return p, ErrInvalid
		}
		var target any
		switch k {
		case "title":
			target = &p.Title
		case "description":
			target = &p.Description
		case "slug":
			target = &p.Slug
		case "content":
			target = &p.Content
		case "visibility":
			target = &p.Visibility
		case "type":
			target = &p.Type
		case "language":
			target = &p.Language
		case "drafted_at":
			target = &p.DraftedAt
		case "published_at":
			target = &p.PublishedAt
		case "account_id", "publisher_id", "publisher", "id", "created_at", "updated_at", "deleted_at", "locked_at":
			return p, ErrInvalid
		case "attachments", "tags", "categories", "embeds", "collection_ids":
			var a []json.RawMessage
			if json.Unmarshal(b, &a) != nil {
				return p, ErrInvalid
			}
			if len(a) > 0 {
				return p, ErrUnsupported
			}
		case "meta":
			var m map[string]json.RawMessage
			if json.Unmarshal(b, &m) != nil {
				return p, ErrInvalid
			}
			if len(m) > 0 {
				return p, ErrUnsupported
			}
		case "auto_chain":
			var v *bool
			if json.Unmarshal(b, &v) != nil {
				return p, ErrInvalid
			}
			if v != nil && *v {
				return p, ErrUnsupported
			}
		case "embed_view", "replied_post_id", "forwarded_post_id", "chained_post_id", "realm_id", "survey_id", "fund_id", "meet_id", "live_stream_id", "notable_day_id", "calendar_event_id", "location_name", "location_address", "location_wkt", "thumbnail_id":
			if !bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
				return p, ErrUnsupported
			}
		default:
			return p, ErrUnsupported
		}
		if target != nil && json.Unmarshal(b, target) != nil {
			return p, ErrInvalid
		}
	}
	if _, e = d.Token(); e != nil {
		return p, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return p, ErrInvalid
	}
	return p, nil
}
func query(q url.Values, allowed ...string) error {
	for k, v := range q {
		if len(v) != 1 {
			return ErrInvalid
		}
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			return ErrUnsupported
		}
	}
	return nil
}
func listOptions(r *http.Request, drafts bool) (ListOptions, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return ListOptions{}, ErrInvalid
	}
	allowed := []string{"offset", "take", "pub"}
	if !drafts {
		allowed = append(allowed, "type", "order", "orderDesc")
	}
	if e = query(q, allowed...); e != nil {
		return ListOptions{}, e
	}
	o := ListOptions{Take: 20, Publisher: q.Get("pub"), Drafts: drafts}
	if strings.Contains(o.Publisher, ",") {
		return o, ErrUnsupported
	}
	if q.Has("offset") {
		o.Offset, e = strconv.Atoi(q.Get("offset"))
		if e != nil {
			return o, ErrInvalid
		}
	}
	if q.Has("take") {
		o.Take, e = strconv.Atoi(q.Get("take"))
		if e != nil {
			return o, ErrInvalid
		}
	}
	if q.Has("type") {
		v, e := strconv.Atoi(q.Get("type"))
		if e != nil {
			return o, ErrInvalid
		}
		o.Type = &v
	}
	if q.Has("orderDesc") {
		v, e := strconv.ParseBool(q.Get("orderDesc"))
		if e != nil {
			return o, ErrInvalid
		}
		o.Ascending = !v
	}
	if v := q.Get("order"); v != "" && v != "date" && v != "created_at" && v != "created" && v != "createdat" {
		return o, ErrUnsupported
	}
	if o.Offset < 0 || o.Take < 1 || o.Take > 100 {
		return o, ErrInvalid
	}
	return o, nil
}

func NewHandler(repo Repository, auth Auth) http.Handler {
	m := http.NewServeMux()
	identity := func(w http.ResponseWriter, r *http.Request, permission string) (string, bool) {
		id, e := auth.Authenticate(r)
		if e != nil {
			failure(w, e)
			return "", false
		}
		if id != "" && !authority.ValidID(id) {
			failure(w, ErrUnauthenticated)
			return "", false
		}
		if permission != "" {
			if id == "" {
				failure(w, ErrUnauthenticated)
				return "", false
			}
			if e = auth.Authorize(r, permission); e != nil {
				failure(w, e)
				return "", false
			}
		}
		return id, true
	}
	list := func(drafts bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			permission := ""
			if drafts {
				permission = "posts.view"
			}
			actor, ok := identity(w, r, permission)
			if !ok {
				return
			}
			o, e := listOptions(r, drafts)
			if e != nil {
				failure(w, e)
				return
			}
			out, total, e := repo.List(r.Context(), actor, o)
			if e != nil {
				failure(w, e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			respond(w, 200, out)
		}
	}
	m.HandleFunc("GET /api/posts", list(false))
	m.HandleFunc("GET /api/posts/drafts", list(true))
	m.HandleFunc("GET /api/posts/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, "")
		if !ok {
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		p, e := repo.Get(r.Context(), actor, r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, p)
	})
	m.HandleFunc("GET /api/posts/{publisher}/{slug}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, "")
		if !ok {
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		p, e := repo.GetSlug(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, p)
	})
	m.HandleFunc("POST /api/posts", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, "posts.create")
		if !ok {
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
		v, e := decode(w, r)
		if e != nil {
			failure(w, e)
			return
		}
		p, e := repo.Create(r.Context(), actor, q.Get("pub"), v)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, p)
	})
	m.HandleFunc("PATCH /api/posts/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, "posts.update")
		if !ok {
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		v, e := decode(w, r)
		if e != nil {
			failure(w, e)
			return
		}
		p, e := repo.Update(r.Context(), actor, r.PathValue("id"), v)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, p)
	})
	m.HandleFunc("POST /api/posts/{id}/publish", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, "posts.create")
		if !ok {
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if e != nil || len(bytes.TrimSpace(b)) != 0 {
			failure(w, ErrInvalid)
			return
		}
		p, e := repo.Publish(r.Context(), actor, r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, p)
	})
	m.HandleFunc("DELETE /api/posts/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, "posts.delete")
		if !ok {
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		if e := repo.Delete(r.Context(), actor, r.PathValue("id")); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		m.ServeHTTP(w, r)
	})
}
