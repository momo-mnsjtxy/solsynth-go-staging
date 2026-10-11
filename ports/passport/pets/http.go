// SPDX-License-Identifier: AGPL-3.0-only
package pets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func RouteMatches(p string) bool         { return p == "/api/pets" || strings.HasPrefix(p, "/api/pets/") }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	var p *Failure
	var a *authority.Failure
	if errors.As(err, &p) {
		send(w, p.Status, p)
	} else if errors.As(err, &a) {
		send(w, a.Status, a)
	} else {
		send(w, 503, ErrUnavailable)
	}
}
func decode(w http.ResponseWriter, r *http.Request, c *Command) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.Body == nil {
		return ErrInvalid
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || !utf8.Valid(body) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return ErrInvalid
		}
		key, ok := token.(string)
		if !ok {
			return ErrInvalid
		}
		switch key {
		case "request_id", "expected_version", "kind", "name", "species", "item_id", "slot", "task_id":
		default:
			return ErrInvalid
		}
		if _, exists := fields[key]; exists {
			return ErrInvalid
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return ErrInvalid
		}
		fields[key] = raw
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	// Null is never a valid replacement for any of the typed command fields.
	for _, v := range fields {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return ErrInvalid
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return ErrInvalid
	}
	d = json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(c) != nil {
		return ErrInvalid
	}
	return nil
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !RouteMatches(r.URL.Path) {
		send(w, 404, map[string]string{"code": "NOT_FOUND"})
		return
	}
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" || r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, authority.ErrForbidden)
			return
		}
	}
	p := r.URL.Path
	method := ""
	fixedKind := ""
	slot := ""
	task := ""
	switch p {
	case "/api/pets/catalog", "/api/pets/shop", "/api/pets/me/inventory", "/api/pets/me/tasks", "/api/pets/me/equipment":
		method = "GET"
	case "/api/pets/me":
		method = "GET, PATCH"
		if r.Method == "PATCH" {
			fixedKind = "rename"
		}
	case "/api/pets/me/adopt":
		method = "POST"
		fixedKind = "adopt"
	case "/api/pets/me/actions", "/api/pets/me/commands":
		method = "POST"
	case "/api/pets/me/purchases":
		method = "POST"
		fixedKind = "buy"
	default:
		parts := strings.Split(strings.TrimPrefix(p, "/api/pets/me/"), "/")
		if len(parts) == 2 && parts[0] == "equipment" && (parts[1] == "head" || parts[1] == "neck") {
			method = "PUT, DELETE"
			fixedKind = "equip"
			slot = parts[1]
		}
		if len(parts) == 3 && parts[0] == "tasks" && parts[2] == "claim" {
			for _, t := range Definitions().Tasks {
				if t.ID == parts[1] {
					method = "POST"
					fixedKind = "claim"
					task = t.ID
				}
			}
		}
	}
	if method == "" {
		send(w, 404, map[string]string{"code": "NOT_FOUND"})
		return
	}
	allowed := false
	for _, m := range strings.Split(method, ", ") {
		if r.Method == m {
			allowed = true
		}
	}
	if !allowed {
		w.Header().Set("Allow", method)
		send(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
		return
	}
	if s == nil || s.Repository == nil || s.Authority == nil || s.Bind == nil {
		failure(w, ErrUnavailable)
		return
	}
	principal, err := s.Authority.AuthenticateRequest(r)
	if err != nil {
		failure(w, err)
		return
	}
	if !authority.ValidID(principal.Account.ID) {
		failure(w, authority.ErrInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(s.Bind(r.Context(), principal), 10*time.Second)
	defer cancel()
	if r.Method == "GET" {
		if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			failure(w, ErrInvalid)
			return
		}
		if p == "/api/pets/catalog" {
			send(w, 200, Definitions())
			return
		}
		if p == "/api/pets/shop" {
			send(w, 200, Definitions().Items)
			return
		}
		state, err := s.Repository.Get(ctx, principal.Account.ID)
		if err != nil {
			failure(w, err)
			return
		}
		switch p {
		case "/api/pets/me/inventory":
			send(w, 200, state.Inventory)
		case "/api/pets/me/equipment":
			send(w, 200, state.Equipment)
		case "/api/pets/me/tasks":
			send(w, 200, struct {
				Daily Daily  `json:"daily"`
				Tasks []Task `json:"tasks"`
			}{state.Daily, Definitions().Tasks})
		default:
			send(w, 200, state)
		}
		return
	}
	var command Command
	if err = decode(w, r, &command); err != nil {
		failure(w, err)
		return
	}
	if fixedKind != "" {
		if command.Kind != "" {
			failure(w, ErrInvalid)
			return
		}
		command.Kind = fixedKind
	}
	if slot != "" {
		if command.Slot != "" {
			failure(w, ErrInvalid)
			return
		}
		command.Slot = slot
		if r.Method == "DELETE" && command.ItemID != "" {
			failure(w, ErrInvalid)
			return
		}
	}
	if task != "" {
		if command.TaskID != "" {
			failure(w, ErrInvalid)
			return
		}
		command.TaskID = task
	}
	if p == "/api/pets/me/actions" && command.Kind != "feed" && command.Kind != "wash" && command.Kind != "play" && command.Kind != "rest" {
		failure(w, ErrInvalid)
		return
	}
	if err = Validate(command); err != nil {
		failure(w, err)
		return
	}
	state, err := s.Repository.Mutate(ctx, principal.Account.ID, command)
	if err != nil {
		failure(w, err)
		return
	}
	send(w, 200, state)
}
