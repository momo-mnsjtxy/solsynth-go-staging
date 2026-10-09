// SPDX-License-Identifier: AGPL-3.0-only
package taxonomy

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	s         *Service
	synthetic bool
}

func NewHandler(s *Service) *Handler          { return &Handler{s: s} }
func NewSyntheticHandler(s *Service) *Handler { return &Handler{s: s, synthetic: true} }

type route struct {
	kind               Kind
	admin              bool
	slug, action, base string
	valid              bool
}

func parseRoute(path string) route {
	if path == "/api/categories/subscriptions" {
		return route{kind: Category, action: "subscriptions", valid: true}
	}
	for _, base := range []string{"/api/admin/posts/tags", "/api/admin/tags", "/api/admin/categories", "/api/posts/tags", "/api/posts/categories"} {
		if path != base && !strings.HasPrefix(path, base+"/") {
			continue
		}
		k := Tag
		if strings.HasSuffix(base, "categories") {
			k = Category
		}
		r := route{kind: k, admin: strings.HasPrefix(base, "/api/admin/"), base: base, valid: true}
		rest := strings.TrimPrefix(path, base)
		if rest == "" {
			return r
		}
		bits := strings.Split(strings.TrimPrefix(rest, "/"), "/")
		if len(bits) > 2 || bits[0] == "" {
			return route{}
		}
		r.slug = bits[0]
		if len(bits) == 2 {
			if bits[1] == "" {
				return route{}
			}
			r.action = bits[1]
		}
		if k == Tag && !r.admin && (r.slug == "quota" && r.action == "" || r.action == "quota") {
			r.action = "quota"
		}
		return r
	}
	return route{}
}
func credentialPresent(r *http.Request) bool {
	for k, v := range r.Header {
		if len(v) == 0 {
			continue
		}
		k = strings.ToLower(k)
		if k == "authorization" || k == "cookie" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-account") || strings.HasPrefix(k, "x-user") || strings.HasPrefix(k, "x-session") || strings.HasPrefix(k, "x-auth") {
			return true
		}
	}
	for k := range r.URL.Query() {
		switch strings.ToLower(k) {
		case "access_token", "token", "account_id", "actor_id":
			return true
		}
	}
	return r.URL.User != nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt := parseRoute(r.URL.Path)
	if !rt.valid {
		http.NotFound(w, r)
		return
	}
	if h == nil || (!h.synthetic && (h.s == nil || h.s.trusted == nil)) {
		writeError(w, ErrPolicyUnverified)
		return
	}
	if h.synthetic {
		w.Header().Set("X-Solsynth-Compatibility", "synthetic-taxonomy-projection; production-parity=unverified")
	} else {
		w.Header().Set("X-Solsynth-Compatibility", "owned-authority-taxonomy-projection-v1; full-entity-parity=unverified")
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.synthetic && credentialPresent(r) {
		writeError(w, ErrForbidden)
		return
	}
	if err := h.s.guard(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	pub := r.URL.Query().Get("pub")
	if rt.action == "subscriptions" {
		if r.Method != "GET" {
			methodNotAllowed(w, "GET")
			return
		}
		q, err := parseQuery(r.URL.Query(), Category, false)
		if err != nil {
			writeError(w, err)
			return
		}
		rows, total, err := h.s.Subscriptions(r.Context(), q.Offset, q.Take)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		writeJSON(w, 200, rows)
		return
	}
	if !rt.admin && (rt.action == "subscription" || rt.action == "subscribe" || rt.action == "unsubscribe") {
		method := "POST"
		if rt.action == "subscription" {
			method = "GET"
		}
		if r.Method != method {
			methodNotAllowed(w, method)
			return
		}
		row, created, err := h.s.Subscription(r.Context(), rt.kind, rt.slug, rt.action)
		if err != nil {
			writeError(w, err)
			return
		}
		if rt.action == "unsubscribe" {
			w.WriteHeader(204)
			return
		}
		status := 200
		if created {
			status = 201
			w.Header().Set("Location", rt.base+"/"+url.PathEscape(rt.slug)+"/subscription")
		}
		writeJSON(w, status, row)
		return
	}
	if rt.action == "quota" {
		if r.Method != "GET" {
			methodNotAllowed(w, "GET")
			return
		}
		v, err := h.s.Quota(r.Context(), pub)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, v)
		return
	}
	if rt.slug == "" {
		switch r.Method {
		case "GET":
			q, err := parseQuery(r.URL.Query(), rt.kind, rt.admin)
			if err != nil {
				writeError(w, err)
				return
			}
			page, err := h.s.List(r.Context(), q)
			if err != nil {
				writeError(w, err)
				return
			}
			rows := make([]any, 0, len(page.Rows))
			for _, row := range page.Rows {
				rows = append(rows, row.Projection())
			}
			w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
			writeJSON(w, 200, rows)
		case "POST":
			if rt.kind == Category && !rt.admin {
				methodNotAllowed(w, "GET")
				return
			}
			keys := []string{"slug", "name"}
			if rt.kind == Tag {
				keys = append(keys, "description")
				if rt.admin {
					keys = append(keys, "owner_publisher_id")
				}
			}
			c, err := decodeChange(w, r, keys...)
			if err != nil {
				writeError(w, err)
				return
			}
			row, err := h.s.Create(r.Context(), rt.kind, c, rt.admin, pub)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Location", rt.base+"/"+url.PathEscape(row.Slug))
			writeJSON(w, 201, row.Projection())
		default:
			allowed := "GET, POST"
			if rt.kind == Category && !rt.admin {
				allowed = "GET"
			}
			methodNotAllowed(w, allowed)
		}
		return
	}
	if rt.action == "" && r.Method == "GET" {
		row, err := h.s.Get(r.Context(), rt.kind, rt.slug, rt.admin)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, row.Projection())
		return
	}
	op := Mutation(rt.action)
	c := Change{}
	var err error
	switch rt.action {
	case "":
		if !rt.admin && rt.kind == Category {
			methodNotAllowed(w, "GET")
			return
		}
		if !rt.admin && r.Method != "PATCH" {
			methodNotAllowed(w, "GET, PATCH")
			return
		}
		if r.Method == "PATCH" {
			op = Update
			keys := []string{"name"}
			if rt.kind == Category {
				keys = append(keys, "slug")
			} else {
				keys = append(keys, "description")
			}
			c, err = decodeChange(w, r, keys...)
		} else if r.Method == "DELETE" {
			op = Delete
		} else {
			methodNotAllowed(w, "GET, PATCH, DELETE")
			return
		}
	case "assign":
		if !rt.admin || rt.kind != Tag {
			http.NotFound(w, r)
			return
		}
		if r.Method == "DELETE" {
			op = Unassign
		} else if r.Method == "POST" {
			op = Assign
			c, err = decodeChange(w, r, "publisher_id")
		} else {
			methodNotAllowed(w, "POST, DELETE")
			return
		}
	case "claim", "release":
		if rt.admin || rt.kind != Tag {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			methodNotAllowed(w, "POST")
			return
		}
	case "protect":
		if rt.kind != Tag {
			http.NotFound(w, r)
			return
		}
		if r.Method != "PATCH" {
			methodNotAllowed(w, "PATCH")
			return
		}
		c, err = decodeChange(w, r, "is_protected")
	case "event":
		if !rt.admin || rt.kind != Tag {
			http.NotFound(w, r)
			return
		}
		if r.Method != "PATCH" {
			methodNotAllowed(w, "PATCH")
			return
		}
		c, err = decodeChange(w, r, "is_event", "ends_at")
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := h.s.Change(r.Context(), rt.kind, rt.slug, op, c, rt.admin, pub)
	if err != nil {
		writeError(w, err)
		return
	}
	if op == Delete {
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 200, row.Projection())
}
func parseQuery(v url.Values, k Kind, admin bool) (Query, error) {
	q := Query{Kind: k, Admin: admin, Search: v.Get("query"), Order: v.Get("order"), Take: 20}
	if admin {
		q.Take = 50
	}
	for name, target := range map[string]*int{"offset": &q.Offset, "take": &q.Take} {
		if values, ok := v[name]; ok {
			if len(values) != 1 || values[0] == "" {
				return q, ErrInvalid
			}
			n, e := strconv.ParseInt(values[0], 10, 32)
			if e != nil {
				return q, ErrInvalid
			}
			*target = int(n)
		}
	}
	if admin && k == Tag {
		if s := v.Get("ownerPublisherId"); s != "" {
			q.OwnerPublisherID = &s
		}
		for name, target := range map[string]**bool{"isProtected": &q.IsProtected, "isEvent": &q.IsEvent, "unowned": &q.Unowned} {
			if values, ok := v[name]; ok {
				if len(values) != 1 {
					return q, ErrInvalid
				}
				s := strings.ToLower(values[0])
				if s != "true" && s != "false" {
					return q, ErrInvalid
				}
				b := s == "true"
				*target = &b
			}
		}
	}
	return q.Normalize()
}
func decodeChange(w http.ResponseWriter, r *http.Request, allowed ...string) (Change, error) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		typ, _, err := mime.ParseMediaType(ct)
		if err != nil || typ != "application/json" {
			return Change{}, Problem("UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415)
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil || raw == nil {
		return Change{}, ErrInvalid
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Change{}, ErrInvalid
	}
	allow := map[string]bool{}
	for _, k := range allowed {
		allow[k] = true
	}
	for k := range raw {
		if !allow[k] {
			return Change{}, ErrInvalid
		}
	}
	var c Change
	for name, target := range map[string]**string{"slug": &c.Slug, "name": &c.Name, "description": &c.Description, "publisher_id": &c.PublisherID, "owner_publisher_id": &c.PublisherID} {
		if b, ok := raw[name]; ok {
			if err := json.Unmarshal(b, target); err != nil {
				return Change{}, ErrInvalid
			}
		}
	}
	for name, target := range map[string]*bool{"is_protected": &c.IsProtected, "is_event": &c.IsEvent} {
		if b, ok := raw[name]; ok {
			if string(b) == "null" {
				return Change{}, ErrInvalid
			}
			if err := json.Unmarshal(b, target); err != nil {
				return Change{}, ErrInvalid
			}
		}
	}
	if b, ok := raw["ends_at"]; ok {
		var t *time.Time
		if err := json.Unmarshal(b, &t); err != nil {
			return Change{}, ErrInvalid
		}
		c.EndsAt = t
	}
	return c, nil
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	w.WriteHeader(405)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	// PostgreSQL timestamps can exceed JSON's supported year range. Complete
	// encoding before committing a successful status or client-visible metadata.
	raw, err := json.Marshal(v)
	if err != nil {
		status = http.StatusServiceUnavailable
		raw = []byte(`{"error":"taxonomy_unavailable"}`)
		w.Header().Del("X-Total")
		w.Header().Del("Location")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}
func writeError(w http.ResponseWriter, err error) {
	var p *DomainError
	if errors.As(err, &p) {
		writeJSON(w, p.Status, map[string]any{"code": p.Code, "message": p.Message, "status": p.Status})
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		w.WriteHeader(404)
	case errors.Is(err, ErrUnauthenticated):
		writeJSON(w, 401, map[string]any{"code": "UNAUTHORIZED", "message": "Authentication is required.", "status": 401})
	case errors.Is(err, ErrForbidden):
		writeJSON(w, 403, map[string]any{"error": "synthetic_authority_denied"})
	case errors.Is(err, ErrInvalid):
		writeJSON(w, 400, map[string]any{"error": "invalid_taxonomy_request"})
	default:
		writeJSON(w, 503, map[string]any{"error": "taxonomy_unavailable"})
	}
}
