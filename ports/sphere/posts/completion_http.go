// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
)

func completionPage(raw string, allowed ...string) (int, int, url.Values, error) {
	q, e := url.ParseQuery(raw)
	if e != nil {
		return 0, 0, nil, ErrInvalid
	}
	keys := append([]string{"offset", "take"}, allowed...)
	if e = query(q, keys...); e != nil {
		return 0, 0, nil, e
	}
	offset, take := 0, 20
	if q.Has("offset") {
		offset, e = strconv.Atoi(q.Get("offset"))
	}
	if e == nil && q.Has("take") {
		take, e = strconv.Atoi(q.Get("take"))
	}
	if e != nil || offset < 0 || take < 1 || take > 100 {
		return 0, 0, nil, ErrInvalid
	}
	return offset, take, q, nil
}

func decodeBoost(w http.ResponseWriter, r *http.Request) (*string, error) {
	if r.Body == nil {
		return nil, nil
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if e != nil || !utf8.Valid(raw) {
		return nil, ErrInvalid
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return nil, &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	if !uniqueEmbedObject(raw) {
		return nil, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || fields["content"] == nil {
		return nil, ErrInvalid
	}
	var in struct {
		Content *string `json:"content"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		return nil, ErrInvalid
	}
	return in.Content, nil
}

func registerCompletion(m *http.ServeMux, repo Repository, identity func(http.ResponseWriter, *http.Request, string) (string, bool)) {
	completion, ok := repo.(CompletionRepository)
	unsupported := func(w http.ResponseWriter) { failure(w, ErrUnsupported) }
	m.HandleFunc("GET /api/posts/featured", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		offset, take, _, e := completionPage(r.URL.RawQuery)
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := completion.Featured(r.Context(), actor, offset, take)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	for _, next := range []bool{false, true} {
		direction := "prev"
		if next {
			direction = "next"
		}
		m.HandleFunc("GET /api/posts/{id}/"+direction, func(w http.ResponseWriter, r *http.Request) {
			actor, valid := identity(w, r, "")
			if !valid {
				return
			}
			if !ok {
				unsupported(w)
				return
			}
			if !authority.ValidID(r.PathValue("id")) {
				failure(w, ErrNotFound)
				return
			}
			opts, e := listOptions(r, false)
			if e != nil {
				failure(w, e)
				return
			}
			out, e := completion.Adjacent(r.Context(), actor, r.PathValue("id"), next, opts)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, out)
		})
	}
	m.HandleFunc("GET /api/posts/reactions/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		offset, take, q, e := completionPage(r.URL.RawQuery, "order")
		if e != nil || q.Get("order") != "" && strings.ToLower(q.Get("order")) != "created" {
			failure(w, ErrInvalid)
			return
		}
		out, total, e := completion.ReactionUser(r.Context(), actor, r.PathValue("name"), offset, take)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	m.HandleFunc("GET /api/posts/{id}/replies/featured", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		out, _, e := completion.Replies(r.Context(), actor, r.PathValue("id"), "featured", 0, 1, false)
		if e != nil {
			failure(w, e)
			return
		}
		if len(out) == 0 {
			failure(w, ErrNotFound)
			return
		}
		respond(w, 200, out[0])
	})
	m.HandleFunc("GET /api/posts/{id}/replies/pinned", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		out, _, e := completion.Replies(r.Context(), actor, r.PathValue("id"), "pinned", 0, 100, false)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("GET /api/posts/{id}/replies", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		offset, take, q, e := completionPage(r.URL.RawQuery, "order", "orderDesc")
		if e != nil {
			failure(w, e)
			return
		}
		if v := q.Get("order"); v != "" && v != "date" && v != "created" && v != "created_at" {
			failure(w, ErrUnsupported)
			return
		}
		ascending := false
		if q.Has("orderDesc") {
			desc, x := strconv.ParseBool(q.Get("orderDesc"))
			if x != nil {
				failure(w, ErrInvalid)
				return
			}
			ascending = !desc
		}
		out, total, e := completion.Replies(r.Context(), actor, r.PathValue("id"), "list", offset, take, ascending)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	m.HandleFunc("POST /api/posts/{id}/boost", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "posts.boost")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		content, e := decodeBoost(w, r)
		if e != nil {
			failure(w, e)
			return
		}
		out, e := completion.BoostPost(r.Context(), actor, r.PathValue("id"), content)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("DELETE /api/posts/{id}/boost", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "posts.boost")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrUnsupported)
			return
		}
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if e != nil || len(bytes.TrimSpace(b)) > 0 {
			failure(w, ErrInvalid)
			return
		}
		if e = completion.UnboostPost(r.Context(), actor, r.PathValue("id")); e != nil {
			failure(w, e)
			return
		}
		respond(w, 204, nil)
	})
	m.HandleFunc("GET /api/posts/{id}/boosts", func(w http.ResponseWriter, r *http.Request) {
		actor, valid := identity(w, r, "")
		if !valid {
			return
		}
		if !ok {
			unsupported(w)
			return
		}
		offset, take, _, e := completionPage(r.URL.RawQuery)
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := completion.PostBoosts(r.Context(), actor, r.PathValue("id"), offset, take)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
}
