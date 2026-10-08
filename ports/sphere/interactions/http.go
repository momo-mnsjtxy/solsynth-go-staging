// SPDX-License-Identifier: AGPL-3.0-only
package interactions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	e = NormalizeError(e)
	status := 503
	var f *Failure
	var a *authority.Failure
	var p *posts.Failure
	var c *accounts.Failure
	switch {
	case errors.As(e, &f):
		status = f.Status
	case errors.As(e, &a):
		status = a.Status
	case errors.As(e, &p):
		status = p.Status
	case errors.As(e, &c):
		status = c.Status
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="posts"`)
	}
	respond(w, status, e)
}
func decode(w http.ResponseWriter, r *http.Request, out any, allowed ...string) error {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if e != nil || !utf8.Valid(b) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		tok, e = d.Token()
		if e != nil {
			return ErrInvalid
		}
		key, ok := tok.(string)
		if !ok || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		recognized := false
		for _, s := range allowed {
			if key == s {
				recognized = true
			}
		}
		if !recognized {
			return ErrInvalid
		}
		var v json.RawMessage
		if d.Decode(&v) != nil || key == "attitude" && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return ErrInvalid
		}
	}
	if _, e = d.Token(); e != nil {
		return ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	if e = json.Unmarshal(b, out); e != nil {
		return ErrInvalid
	}
	return nil
}
func emptyBody(w http.ResponseWriter, r *http.Request) error {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if e != nil || len(bytes.TrimSpace(b)) > 0 {
		return ErrInvalid
	}
	return nil
}
func page(r *http.Request, extras ...string) (Page, error) {
	p := Page{Take: 20}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return p, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 {
			return p, ErrInvalid
		}
		allow := k == "take" || k == "offset"
		for _, x := range extras {
			if k == x {
				allow = true
			}
		}
		if !allow {
			return p, ErrUnsupported
		}
	}
	if q.Has("take") {
		p.Take, e = strconv.Atoi(q.Get("take"))
		if e != nil {
			return p, ErrInvalid
		}
	}
	if q.Has("offset") {
		p.Offset, e = strconv.Atoi(q.Get("offset"))
		if e != nil {
			return p, ErrInvalid
		}
	}
	p.Symbol = q.Get("symbol")
	p.Order = q.Get("order")
	p.Query = q.Get("query")
	if !ValidPage(p) {
		return p, ErrInvalid
	}
	return p, nil
}
func NewHandler(repo Repository, auth posts.Auth) http.Handler {
	mux := http.NewServeMux()
	identity := func(w http.ResponseWriter, r *http.Request, required bool, permission string) (string, bool) {
		actor, e := auth.Authenticate(r)
		if e != nil {
			failure(w, e)
			return "", false
		}
		if actor == "" && required || actor != "" && !authority.ValidID(actor) {
			failure(w, posts.ErrUnauthenticated)
			return "", false
		}
		if permission != "" {
			if e = auth.Authorize(r, permission); e != nil {
				failure(w, e)
				return "", false
			}
		}
		return actor, true
	}
	noquery := func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /api/posts/{id}/reactions", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "posts.react")
		if !ok || !noquery(w, r) {
			return
		}
		var v ReactionRequest
		if e := decode(w, r, &v, "symbol", "attitude"); e != nil {
			failure(w, e)
			return
		}
		if e := ValidateReaction(v); e != nil {
			failure(w, e)
			return
		}
		out, removed, e := repo.React(r.Context(), actor, r.PathValue("id"), v)
		if e != nil {
			failure(w, e)
			return
		}
		if removed {
			respond(w, 204, nil)
		} else {
			respond(w, 200, out)
		}
	})
	mux.HandleFunc("GET /api/posts/{id}/reactions", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		p, e := page(r, "symbol", "order")
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := repo.Reactions(r.Context(), actor, r.PathValue("id"), p)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	mux.HandleFunc("POST /api/posts/{id}/bookmark", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "posts.bookmark")
		if !ok || !noquery(w, r) {
			return
		}
		if e := emptyBody(w, r); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Bookmark(r.Context(), actor, r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("DELETE /api/posts/{id}/bookmark", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "posts.bookmark")
		if !ok || !noquery(w, r) {
			return
		}
		if e := emptyBody(w, r); e != nil {
			failure(w, e)
			return
		}
		if e := repo.Unbookmark(r.Context(), actor, r.PathValue("id")); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	mux.HandleFunc("GET /api/posts/{id}/bookmark", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "")
		if !ok || !noquery(w, r) {
			return
		}
		out, e := repo.BookmarkStatus(r.Context(), actor, r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("GET /api/posts/bookmarks", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "posts.bookmark")
		if !ok {
			return
		}
		p, e := page(r, "order")
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := repo.Bookmarks(r.Context(), actor, p)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	collectionRoot := "/api/publishers/{publisher}/collections"
	mux.HandleFunc("GET /api/collections", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		p, e := page(r, "query")
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := repo.Collections(r.Context(), actor, "", p)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	mux.HandleFunc("GET "+collectionRoot, func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok || !noquery(w, r) {
			return
		}
		out, _, e := repo.Collections(r.Context(), actor, r.PathValue("publisher"), Page{Take: 100})
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("GET "+collectionRoot+"/{slug}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok || !noquery(w, r) {
			return
		}
		out, e := repo.Collection(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	for _, create := range []bool{true, false} {
		pattern := "POST " + collectionRoot
		if !create {
			pattern = "PATCH " + collectionRoot + "/{slug}"
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor, ok := identity(w, r, true, "")
			if !ok || !noquery(w, r) {
				return
			}
			keys := []string{"name", "description", "background_id", "icon_id"}
			if create {
				keys = append(keys, "slug")
			}
			var v CollectionRequest
			if e := decode(w, r, &v, keys...); e != nil {
				failure(w, e)
				return
			}
			var out Collection
			var e error
			if create {
				out, e = repo.CreateCollection(r.Context(), actor, r.PathValue("publisher"), v)
			} else {
				out, e = repo.UpdateCollection(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"), v)
			}
			if e != nil {
				failure(w, e)
				return
			}
			status := 200
			if create {
				status = 201
				w.Header().Set("Location", "/api/publishers/"+url.PathEscape(out.Publisher.Name)+"/collections/"+url.PathEscape(out.Slug))
			}
			respond(w, status, out)
		})
	}
	mux.HandleFunc("DELETE "+collectionRoot+"/{slug}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "")
		if !ok || !noquery(w, r) {
			return
		}
		if e := emptyBody(w, r); e != nil {
			failure(w, e)
			return
		}
		if e := repo.DeleteCollection(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug")); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	mux.HandleFunc("GET "+collectionRoot+"/{slug}/posts", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		p, e := page(r)
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := repo.CollectionPosts(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"), p)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	mux.HandleFunc("POST "+collectionRoot+"/{slug}/posts", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "post.collections.posts.manage")
		if !ok || !noquery(w, r) {
			return
		}
		var v struct {
			PostID string `json:"post_id"`
			Order  *int   `json:"order"`
		}
		if e := decode(w, r, &v, "post_id", "order"); e != nil {
			failure(w, e)
			return
		}
		if v.Order != nil {
			failure(w, ErrOrder)
			return
		}
		if e := repo.AddCollectionPosts(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"), []string{v.PostID}, false); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	for _, remove := range []bool{false, true} {
		pattern := "POST " + collectionRoot + "/{slug}/posts/batch"
		if remove {
			pattern += "/remove"
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor, ok := identity(w, r, true, "post.collections.posts.manage")
			if !ok || !noquery(w, r) {
				return
			}
			var v struct {
				PostIDs []string `json:"post_ids"`
			}
			if e := decode(w, r, &v, "post_ids"); e != nil {
				failure(w, e)
				return
			}
			var e error
			if remove {
				e = repo.RemoveCollectionPosts(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"), v.PostIDs)
			} else {
				e = repo.AddCollectionPosts(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"), v.PostIDs, true)
			}
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 204, nil)
		})
	}
	mux.HandleFunc("DELETE "+collectionRoot+"/{slug}/posts/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "post.collections.posts.manage")
		if !ok || !noquery(w, r) {
			return
		}
		if e := emptyBody(w, r); e != nil {
			failure(w, e)
			return
		}
		if e := repo.RemoveCollectionPosts(r.Context(), actor, r.PathValue("publisher"), r.PathValue("slug"), []string{r.PathValue("id")}); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	mux.HandleFunc("PUT "+collectionRoot+"/{slug}/posts/reorder", func(w http.ResponseWriter, r *http.Request) {
		_, ok := identity(w, r, true, "post.collections.posts.manage")
		if !ok {
			return
		}
		failure(w, ErrReorder)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// RoutePatterns lets the unified host dispatch exact interaction routes before
// its post slug fallback, without mounting a second identity/authentication layer.
var RoutePatterns = []string{
	"POST /api/posts/{id}/reactions", "GET /api/posts/{id}/reactions", "POST /api/posts/{id}/bookmark", "DELETE /api/posts/{id}/bookmark", "GET /api/posts/{id}/bookmark", "GET /api/posts/bookmarks", "GET /api/collections",
	"GET /api/publishers/{publisher}/collections", "POST /api/publishers/{publisher}/collections", "GET /api/publishers/{publisher}/collections/{slug}", "PATCH /api/publishers/{publisher}/collections/{slug}", "DELETE /api/publishers/{publisher}/collections/{slug}",
	"GET /api/publishers/{publisher}/collections/{slug}/posts", "POST /api/publishers/{publisher}/collections/{slug}/posts", "POST /api/publishers/{publisher}/collections/{slug}/posts/batch", "POST /api/publishers/{publisher}/collections/{slug}/posts/batch/remove", "DELETE /api/publishers/{publisher}/collections/{slug}/posts/{id}", "PUT /api/publishers/{publisher}/collections/{slug}/posts/reorder",
}
