// SPDX-License-Identifier: AGPL-3.0-only
package timeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
	e = posts.NormalizeError(e)
	status := 503
	var p *posts.Failure
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
		w.Header().Set("WWW-Authenticate", `Bearer realm="timeline"`)
	}
	respond(w, status, e)
}
func query(r *http.Request, allowed ...string) (url.Values, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, ErrInvalid
	}
	for k, vs := range q {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			return nil, posts.ErrUnsupported
		}
		if len(vs) != 1 && k != "tags" && k != "categories" {
			return nil, ErrInvalid
		}
	}
	return q, nil
}
func integer(q url.Values, key string, d int) (int, error) {
	if !q.Has(key) {
		return d, nil
	}
	v, e := strconv.Atoi(q.Get(key))
	if e != nil {
		return 0, ErrInvalid
	}
	return v, nil
}
func boolean(q url.Values, key string, d bool) (bool, error) {
	if !q.Has(key) {
		return d, nil
	}
	v, e := strconv.ParseBool(q.Get(key))
	if e != nil {
		return false, ErrInvalid
	}
	return v, nil
}
func decode(w http.ResponseWriter, r *http.Request, v any, optional bool) error {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if e != nil || !utf8.Valid(b) {
		return ErrInvalid
	}
	if len(bytes.TrimSpace(b)) == 0 {
		if optional {
			return nil
		}
		return ErrInvalid
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return &posts.Failure{Code: "UNSUPPORTED_MEDIA_TYPE", Message: "JSON is required.", Status: 415}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = uniqueJSON(d, true); e != nil {
		return e
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrInvalid
	}
	return nil
}

// Reject duplicate keys at every nesting level; case variants would otherwise
// be accepted as the same Go field by encoding/json's case-insensitive decoder.
func uniqueJSON(d *json.Decoder, objectRequired bool) error {
	token, e := d.Token()
	if e != nil {
		return ErrInvalid
	}
	delim, isDelim := token.(json.Delim)
	if objectRequired && (!isDelim || delim != '{') {
		return ErrInvalid
	}
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return ErrInvalid
			}
			s, ok := key.(string)
			if !ok {
				return ErrInvalid
			}
			s = strings.ToLower(s)
			if seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if e = uniqueJSON(d, false); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e = uniqueJSON(d, false); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func searchOptions(r *http.Request, related bool) (SearchOptions, error) {
	allowed := []string{"offset", "take", "order", "orderDesc"}
	if !related {
		allowed = append(allowed, "pub", "type", "query", "tags", "categories", "periodStart", "periodEnd", "replies", "media", "shuffle", "pinned", "searchEngine", "realm", "mentioned")
	}
	q, e := query(r, allowed...)
	if e != nil {
		return SearchOptions{}, e
	}
	o := SearchOptions{Publisher: strings.TrimSpace(q.Get("pub")), Query: q.Get("query"), Order: strings.ToLower(strings.TrimSpace(q.Get("order"))), Tags: q["tags"], Categories: q["categories"]}
	o.Offset, e = integer(q, "offset", 0)
	if e != nil {
		return o, e
	}
	o.Take, e = integer(q, "take", 20)
	if e != nil {
		return o, e
	}
	desc, e := boolean(q, "orderDesc", true)
	if e != nil {
		return o, e
	}
	o.Ascending = !desc
	if o.Offset < 0 || o.Offset > 1000000 || o.Take < 1 || o.Take > 100 {
		return o, ErrInvalid
	}
	if q.Has("type") {
		v, e := integer(q, "type", 0)
		if e != nil {
			return o, e
		}
		o.Type = &v
	}
	if q.Has("replies") {
		v, e := boolean(q, "replies", false)
		if e != nil {
			return o, e
		}
		o.Replies = &v
	}
	for _, p := range []struct {
		key string
		dst **time.Time
	}{{"periodStart", &o.Start}, {"periodEnd", &o.End}} {
		if q.Has(p.key) {
			v, e := strconv.ParseInt(q.Get(p.key), 10, 64)
			if e != nil {
				return o, ErrInvalid
			}
			t := time.Unix(v, 0).UTC()
			if t.Year() < 1 || t.Year() > 9999 {
				return o, ErrInvalid
			}
			*p.dst = &t
		}
	}
	for _, k := range []string{"media", "shuffle", "pinned"} {
		if q.Has(k) {
			v, e := boolean(q, k, false)
			if e != nil {
				return o, e
			}
			if v {
				return o, posts.ErrUnsupported
			}
		}
	}
	if q.Get("realm") != "" || q.Get("mentioned") != "" {
		return o, posts.ErrUnsupported
	}
	switch strings.ToLower(q.Get("searchEngine")) {
	case "", "fulltext", "full-text", "full_text":
	default:
		return o, posts.ErrUnsupported
	}
	return o, nil
}
func NewHandler(repo Repository, auth posts.Auth) http.Handler {
	m := http.NewServeMux()
	identity := func(w http.ResponseWriter, r *http.Request, required bool, permission string) (string, bool) {
		actor, e := auth.Authenticate(r)
		if e != nil {
			failure(w, e)
			return "", false
		}
		if actor != "" && !authority.ValidID(actor) || required && actor == "" {
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
	m.HandleFunc("GET /api/timeline", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		q, e := query(r, "cursor", "filter", "pub", "collection", "take", "mode", "aggressive")
		if e != nil {
			failure(w, e)
			return
		}
		take, e := integer(q, "take", 20)
		if e != nil {
			failure(w, e)
			return
		}
		agg, e := boolean(q, "aggressive", true)
		if e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Feed(r.Context(), actor, Options{Take: take, Cursor: q.Get("cursor"), Mode: sphere.Mode(strings.ToLower(strings.TrimSpace(q.Get("mode")))), Filter: strings.ToLower(strings.TrimSpace(q.Get("filter"))), Publisher: strings.TrimSpace(q.Get("pub")), Collection: strings.TrimSpace(q.Get("collection")), Aggressive: agg})
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	for _, method := range []string{"GET", "PUT"} {
		m.HandleFunc(method+" /api/posts/watch", func(w http.ResponseWriter, r *http.Request) {
			permission := ""
			if method == "PUT" {
				permission = "post.subscriptions.manage"
			}
			actor, ok := identity(w, r, true, permission)
			if !ok {
				return
			}
			if _, e := query(r); e != nil {
				failure(w, e)
				return
			}
			var out []sphere.WatchPreference
			var e error
			if method == "GET" {
				out, e = repo.WatchPreferences(r.Context(), actor)
			} else {
				type filters struct {
					Reactions *bool `json:"reactions"`
					Replies   *bool `json:"replies"`
					Chains    *bool `json:"chains"`
					Forwards  *bool `json:"forwards"`
					Edits     *bool `json:"edits"`
				}
				var body struct {
					Bookmark *filters `json:"bookmark"`
					Reaction *filters `json:"reaction"`
					Reply    *filters `json:"reply"`
				}
				if e = decode(w, r, &body, false); e != nil {
					failure(w, e)
					return
				}
				updates := []sphere.WatchPreferenceUpdate{}
				for _, v := range []struct {
					source sphere.WatchSource
					f      *filters
				}{{sphere.WatchBookmark, body.Bookmark}, {sphere.WatchReaction, body.Reaction}, {sphere.WatchReply, body.Reply}} {
					if v.f != nil {
						updates = append(updates, sphere.WatchPreferenceUpdate{Source: v.source, NotifyReactions: v.f.Reactions, NotifyReplies: v.f.Replies, NotifyChains: v.f.Chains, NotifyForwards: v.f.Forwards, NotifyEdits: v.f.Edits})
					}
				}
				out, e = repo.PatchWatch(r.Context(), actor, updates)
			}
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, out)
		})
	}
	m.HandleFunc("GET /api/posts", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		o, e := searchOptions(r, false)
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := repo.Search(r.Context(), actor, o)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	for _, kind := range []string{"replies", "forwards"} {
		m.HandleFunc("GET /api/posts/{id}/"+kind, func(w http.ResponseWriter, r *http.Request) {
			actor, ok := identity(w, r, false, "")
			if !ok {
				return
			}
			o, e := searchOptions(r, true)
			if e != nil {
				failure(w, e)
				return
			}
			out, total, e := repo.Related(r.Context(), actor, r.PathValue("id"), kind, o)
			if e != nil {
				failure(w, e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			respond(w, 200, out)
		})
	}
	m.HandleFunc("GET /api/posts/{id}/replies/threaded", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		o, e := searchOptions(r, true)
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := repo.ThreadedReplies(r.Context(), actor, r.PathValue("id"), o)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	m.HandleFunc("GET /api/posts/{id}/thread", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		q, e := query(r, "ancestors", "ancestorLimit", "take")
		if e != nil {
			failure(w, e)
			return
		}
		a, e := boolean(q, "ancestors", true)
		if e != nil {
			failure(w, e)
			return
		}
		limit, e := integer(q, "ancestorLimit", 50)
		if e != nil {
			failure(w, e)
			return
		}
		take, e := integer(q, "take", 20)
		if e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Thread(r.Context(), actor, r.PathValue("id"), ThreadOptions{Ancestors: a, AncestorLimit: limit, Take: take})
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("GET /api/posts/{id}/chain", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, false, "")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Chain(r.Context(), actor, r.PathValue("id"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("GET /api/publishers/{name}/subscription", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.SubscriptionStatus(r.Context(), actor, r.PathValue("name"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	for _, op := range []string{"subscribe", "unsubscribe"} {
		m.HandleFunc("POST /api/publishers/{name}/"+op, func(w http.ResponseWriter, r *http.Request) {
			actor, ok := identity(w, r, true, "publishers.subscriptions.manage")
			if !ok {
				return
			}
			if _, e := query(r); e != nil {
				failure(w, e)
				return
			}
			var empty struct{}
			if e := decode(w, r, &empty, true); e != nil {
				failure(w, e)
				return
			}
			var out SubscriptionStatus
			var e error
			if op == "subscribe" {
				out, e = repo.Subscribe(r.Context(), actor, r.PathValue("name"))
			} else {
				out, e = repo.Unsubscribe(r.Context(), actor, r.PathValue("name"))
			}
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, out)
		})
	}
	m.HandleFunc("GET /api/publishers/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "")
		if !ok {
			return
		}
		q, e := query(r, "offset", "take", "order")
		if e != nil {
			failure(w, e)
			return
		}
		if q.Get("order") != "" && q.Get("order") != "created_at" {
			failure(w, posts.ErrUnsupported)
			return
		}
		offset, e := integer(q, "offset", 0)
		if e != nil {
			failure(w, e)
			return
		}
		take, e := integer(q, "take", 20)
		if e != nil {
			failure(w, e)
			return
		}
		out, total, e := repo.Subscriptions(r.Context(), actor, offset, take)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		respond(w, 200, out)
	})
	m.HandleFunc("GET /api/publishers/{name}/subscription/requests", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.FollowRequests(r.Context(), actor, r.PathValue("name"))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	for _, op := range []string{"approve", "reject"} {
		m.HandleFunc("POST /api/publishers/{name}/subscription/requests/{requestId}/"+op, func(w http.ResponseWriter, r *http.Request) {
			actor, ok := identity(w, r, true, "publishers.subscriptions.manage")
			if !ok {
				return
			}
			if _, e := query(r); e != nil {
				failure(w, e)
				return
			}
			var body struct {
				Reason *string `json:"reason"`
			}
			if e := decode(w, r, &body, true); e != nil {
				failure(w, e)
				return
			}
			if op == "approve" && body.Reason != nil {
				failure(w, ErrInvalid)
				return
			}
			out, e := repo.DecideFollow(r.Context(), actor, r.PathValue("name"), r.PathValue("requestId"), op == "approve", body.Reason)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, out)
		})
	}
	m.HandleFunc("GET /api/timeline/discovery/profile", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Discovery(r.Context(), actor)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("POST /api/timeline/discovery/uninterested", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "timelines.feedback")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		var body PreferenceRequest
		if e := decode(w, r, &body, false); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Suppress(r.Context(), actor, body)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("DELETE /api/timeline/discovery/uninterested", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "timelines.feedback")
		if !ok {
			return
		}
		q, e := query(r, "kind", "referenceId")
		if e != nil {
			failure(w, e)
			return
		}
		kind, ref := q.Get("kind"), q.Get("referenceId")
		if len(q) == 0 {
			var body struct {
				Kind        string `json:"kind"`
				ReferenceID string `json:"reference_id"`
			}
			if e = decode(w, r, &body, false); e != nil {
				failure(w, e)
				return
			}
			kind, ref = body.Kind, body.ReferenceID
		} else {
			var empty struct{}
			if e = decode(w, r, &empty, true); e != nil {
				failure(w, e)
				return
			}
		}
		if e = repo.Unsuppress(r.Context(), actor, kind, ref); e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, nil)
	})
	m.HandleFunc("PUT /api/timeline/discovery/weights", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "timelines.weights.manage")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		var body WeightRequest
		if e := decode(w, r, &body, false); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.AdjustWeight(r.Context(), actor, body)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("POST /api/timeline/discovery/feedback", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "timelines.feedback")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		var body FeedbackRequest
		if e := decode(w, r, &body, false); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.Feedback(r.Context(), actor, body)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	m.HandleFunc("POST /api/timeline/discovery/reset", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := identity(w, r, true, "timelines.reset")
		if !ok {
			return
		}
		if _, e := query(r); e != nil {
			failure(w, e)
			return
		}
		var empty struct{}
		if e := decode(w, r, &empty, true); e != nil {
			failure(w, e)
			return
		}
		out, e := repo.ResetInterests(r.Context(), actor)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || auth == nil {
			failure(w, posts.ErrUnavailable)
			return
		}
		m.ServeHTTP(w, r)
	})
}

// Handles identifies owned routes for a host composing this handler with the
// canonical post/account handlers. It does not authenticate or rewrite URLs.
func Handles(r *http.Request) bool {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if p == "/api/posts/watch" {
		return true
	}
	if p == "/api/timeline" || strings.HasPrefix(p, "/api/timeline/") {
		return true
	}
	if r.Method == "GET" && p == "/api/posts" {
		return true
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "posts" {
		switch parts[3] {
		case "replies", "forwards", "thread", "chain":
			return true
		}
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "publishers" {
		if parts[2] == "subscriptions" {
			return true
		}
		if len(parts) >= 4 {
			switch parts[3] {
			case "subscription", "subscribe", "unsubscribe":
				return true
			}
		}
	}
	return false
}
