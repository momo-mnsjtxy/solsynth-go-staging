// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/media"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func RouteMatches(p string) bool {
	if RoomCommandsRoute(p) {
		return true
	}
	for _, base := range []string{"/api/bots", "/api/private/bots", "/api/miniapps", "/api/private/miniapps"} {
		if p == base || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Solsynth-Projection", "owned-app-bots-v1")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error, mini bool) {
	var a *authority.Failure
	if errors.As(e, &a) {
		send(w, a.Status, a)
		return
	}
	if mini && errors.Is(e, ErrNotFound) {
		send(w, 404, &Failure{"DEV_MINI_APP_NOT_FOUND", "Miniapp not found.", 404})
		return
	}
	var m *media.Failure
	if errors.As(e, &m) {
		send(w, m.Status, &Failure{m.Code, "Canonical media operation failed.", m.Status})
		return
	}
	var f *Failure
	if errors.As(e, &f) {
		send(w, f.Status, f)
		return
	}
	send(w, 503, ErrUnavailable)
}
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 12 {
		return ErrInvalid
	}
	token, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			name, ok := k.(string)
			folded := strings.ToLower(name)
			if !ok || seen[folded] {
				return ErrInvalid
			}
			seen[folded] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return ErrInvalid
	}
	_, e = d.Token()
	return e
}
func body(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, ErrInvalid
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 131072))
	if e != nil {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if e = uniqueJSON(d, 0); e != nil {
		return nil, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, ErrInvalid
	}
	return raw, nil
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	raw, e := body(w, r)
	if e != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrInvalid
	}
	return nil
}
func empty(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	raw, e := body(w, r)
	if e != nil || string(bytes.TrimSpace(raw)) != "{}" {
		return ErrInvalid
	}
	return nil
}
func page(q url.Values) (int, int, error) {
	offset, take := 0, 20
	var e error
	if raw := q.Get("offset"); raw != "" {
		offset, e = strconv.Atoi(raw)
		if e != nil {
			return 0, 0, ErrInvalid
		}
	}
	if raw := q.Get("take"); raw != "" {
		take, e = strconv.Atoi(raw)
		if e != nil {
			return 0, 0, ErrInvalid
		}
	}
	if offset < 0 || offset > 100000 || take < 1 || take > 100 {
		return 0, 0, ErrInvalid
	}
	return offset, take, nil
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable, false)
		return
	}
	if !RouteMatches(r.URL.Path) {
		w.WriteHeader(404)
		return
	}
	mini := strings.Contains(r.URL.Path, "/miniapps")
	fail := func(e error) { failure(w, e, mini) }
	if path.Clean(r.URL.Path) != r.URL.Path || r.URL.RawPath != "" || strings.ContainsAny(r.URL.Path, "\\") || r.URL.User != nil {
		fail(ErrInvalid)
		return
	}
	authPresent := false
	for name := range r.Header {
		k := strings.ToLower(name)
		if k == "authorization" {
			authPresent = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-publisher-role" || k == "x-app-secret" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			fail(ErrForbidden)
			return
		}
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		fail(ErrInvalid)
		return
	}
	private := strings.HasPrefix(r.URL.Path, "/api/private/")
	for k, v := range q {
		allowed := mini && (k == "offset" || k == "take" || k == "search") || private && (k == "dev" || k == "proj" || k == "app" || k == "offset" || k == "take")
		if !allowed || len(v) != 1 {
			fail(ErrInvalid)
			return
		}
	}
	offset, take, e := page(q)
	if e != nil {
		fail(e)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	botReplyRoute := !private && len(parts) == 4 && parts[0] == "api" && parts[1] == "bots" && parts[3] == "reply"
	ctx := r.Context()
	actor := ""
	if (private || authPresent || RoomCommandsRoute(r.URL.Path)) && !botReplyRoute {
		if s.Authority == nil {
			fail(authority.ErrUnavailable)
			return
		}
		p, e := s.Authority.AuthenticateRequest(r)
		if e != nil {
			fail(e)
			return
		}
		if _, e = s.Authority.AuthenticatePrincipal(ctx, p); e != nil {
			fail(e)
			return
		}
		actor = p.Account.ID
		if s.Bind != nil {
			ctx = s.Bind(ctx, p)
		}
	}
	if RoomCommandsRoute(r.URL.Path) {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(405)
			return
		}
		repo, ok := s.Repository.(ExecutionRepository)
		if !ok {
			fail(ErrUnsupported)
			return
		}
		if !authority.ValidID(parts[2]) {
			fail(ErrInvalid)
			return
		}
		v, e := repo.RoomCommands(ctx, actor, parts[2])
		if e != nil {
			fail(e)
			return
		}
		send(w, 200, v)
		return
	}
	if botReplyRoute {
		keys := r.Header.Values("X-Api-Key")
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return
		}
		if authPresent || len(keys) != 1 || strings.TrimSpace(keys[0]) != keys[0] || keys[0] == "" || !authority.ValidID(parts[2]) {
			fail(ErrForbidden)
			return
		}
		var request ReplyRequest
		if e = decode(w, r, &request); e != nil {
			fail(e)
			return
		}
		out, e := s.Repository.Reply(ctx, parts[2], keys[0], request)
		if e != nil {
			fail(e)
			return
		}
		send(w, 201, out)
		return
	}
	if !private {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(405)
			return
		}
		if mini {
			if len(parts) == 2 {
				rows, total, e := s.Repository.DiscoverMiniApps(ctx, offset, take, q.Get("search"))
				if e != nil {
					fail(e)
					return
				}
				w.Header().Set("X-Total", strconv.FormatInt(total, 10))
				send(w, 200, rows)
				return
			}
			if len(parts) == 3 || len(parts) == 4 && parts[3] == "package" {
				v, e := s.Repository.PublicMiniApp(ctx, parts[2])
				if e != nil {
					fail(e)
					return
				}
				if len(parts) == 4 {
					if v.PackageURL == nil {
						send(w, 404, &Failure{"DEV_MINI_APP_NO_PACKAGE", "Miniapp has no published package.", 404})
						return
					}
					file, reader, e := s.Repository.OpenPackage(ctx, parts[2])
					if e != nil {
						fail(e)
						return
					}
					defer reader.Close()
					w.Header().Set("Cache-Control", "no-store")
					w.Header().Set("Content-Type", "application/zip")
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": v.Slug + ".zip"}))
					w.Header().Set("ETag", strconv.Quote(file.SHA256))
					http.ServeContent(w, r, file.Name, file.UpdatedAt, reader)
					return
				}
				send(w, 200, v)
				return
			}
		} else if len(parts) == 3 || len(parts) == 4 {
			var out any
			var e error
			switch {
			case len(parts) == 3:
				out, e = s.Repository.PublicBot(ctx, parts[2])
			case parts[3] == "developer":
				out, e = s.Repository.PublicDeveloper(ctx, parts[2])
			case parts[3] == "chat" || parts[3] == "commands":
				var c ChatConfig
				c, e = s.Repository.PublicChat(ctx, parts[2])
				out = c
				if parts[3] == "commands" {
					out = c.Commands
				}
			default:
				w.WriteHeader(404)
				return
			}
			if e != nil {
				fail(e)
				return
			}
			send(w, 200, out)
			return
		}
		w.WriteHeader(404)
		return
	}
	scope := apps.Scope{Actor: actor, Developer: q.Get("dev"), Project: q.Get("proj"), App: q.Get("app")}
	if scope.Developer == "" || !authority.ValidID(scope.Project) || (scope.App != "" && !authority.ValidID(scope.App)) || (!mini && scope.App == "") {
		fail(ErrInvalid)
		return
	}
	id := ""
	if len(parts) > 3 {
		id = parts[3]
		if !authority.ValidID(id) {
			fail(ErrInvalid)
			return
		}
	}
	if mini {
		if scope.App == "" {
			resolver, ok := s.Repository.(MiniScopeResolver)
			if !ok {
				fail(ErrUnsupported)
				return
			}
			scope, e = resolver.ResolveMiniScope(ctx, scope, id)
			if e != nil {
				fail(e)
				return
			}
		}
		s.miniHTTP(w, r, ctx, scope, id, parts, offset, take)
		return
	}
	if len(parts) == 3 {
		switch r.Method {
		case "GET":
			v, total, e := s.Repository.ListBots(ctx, scope, offset, take)
			if e != nil {
				fail(e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			send(w, 200, v)
		case "POST":
			var p Patch
			if e := decode(w, r, &p); e != nil {
				fail(e)
				return
			}
			v, e := s.Repository.Bots(ctx, scope, "", &p, "create")
			if e != nil {
				fail(e)
				return
			}
			send(w, 200, v[0])
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(405)
		}
		return
	}
	if len(parts) == 4 {
		action := "read"
		var p *Patch
		switch r.Method {
		case "GET":
		case "PATCH":
			action = "update"
			p = &Patch{}
			if e := decode(w, r, p); e != nil {
				fail(e)
				return
			}
		case "DELETE":
			action = "delete"
			if e := empty(w, r); e != nil {
				fail(e)
				return
			}
		default:
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			w.WriteHeader(405)
			return
		}
		v, e := s.Repository.Bots(ctx, scope, id, p, action)
		if e != nil {
			fail(e)
			return
		}
		if action == "delete" {
			send(w, 204, nil)
		} else {
			send(w, 200, v[0])
		}
		return
	}
	if len(parts) >= 5 && parts[4] == "execution" {
		s.executionHTTP(w, r, ctx, scope, id, parts)
		return
	}
	if len(parts) >= 5 && parts[4] == "keys" {
		if len(parts) > 7 || len(parts) == 7 && parts[6] != "rotate" {
			w.WriteHeader(404)
			return
		}
		keyID := ""
		if len(parts) >= 6 {
			keyID = parts[5]
			if !authority.ValidID(keyID) {
				fail(ErrInvalid)
				return
			}
		}
		action := ""
		var request *KeyRequest
		switch {
		case r.Method == "GET" && len(parts) <= 6:
			action = "read"
		case r.Method == "POST" && len(parts) == 5:
			action, request = "create", &KeyRequest{}
			if e = decode(w, r, request); e != nil {
				fail(e)
				return
			}
		case r.Method == "POST" && len(parts) == 7:
			action = "rotate"
			if r.ContentLength != 0 {
				request = &KeyRequest{}
				if e = decode(w, r, request); e != nil {
					fail(e)
					return
				}
			}
		case r.Method == "DELETE" && len(parts) == 6:
			action = "delete"
			if e = empty(w, r); e != nil {
				fail(e)
				return
			}
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			w.WriteHeader(405)
			return
		}
		keys, e := s.Repository.Keys(ctx, scope, id, keyID, request, action)
		if e != nil {
			fail(e)
			return
		}
		if action == "delete" {
			send(w, 204, nil)
		} else if keyID != "" || action == "create" {
			if len(keys) != 1 {
				fail(ErrNotFound)
				return
			}
			send(w, 200, keys[0])
		} else {
			send(w, 200, keys)
		}
		return
	}
	if len(parts) == 5 && parts[4] == "chat" && r.Method == "GET" {
		v, e := s.Repository.Chat(ctx, scope, id, nil, false)
		if e != nil {
			fail(e)
			return
		}
		send(w, 200, v)
		return
	}
	if len(parts) == 5 && parts[4] == "chat" && r.Method == "PUT" || len(parts) == 6 && parts[4] == "chat" && parts[5] == "manifest" && r.Method == "POST" {
		var p ManifestPatch
		commands := []develop.BotCommand{}
		p.Commands = &commands
		if len(parts) == 5 {
			var full struct {
				ManifestPatch
				ID        string     `json:"id"`
				CreatedAt *time.Time `json:"created_at"`
				UpdatedAt *time.Time `json:"updated_at"`
			}
			full.ManifestPatch = p
			if e := decode(w, r, &full); e != nil || full.ID != "" && full.ID != id {
				fail(ErrInvalid)
				return
			}
			p = full.ManifestPatch
		} else {
			if e := decode(w, r, &p); e != nil {
				fail(e)
				return
			}
		}
		v, e := s.Repository.Chat(ctx, scope, id, &p, len(parts) == 5)
		if e != nil {
			fail(e)
			return
		}
		send(w, 200, v)
		return
	}
	if (len(parts) == 5 || len(parts) == 6) && parts[4] == "commands" {
		name := ""
		if len(parts) == 6 {
			name = parts[5]
		}
		action := "read"
		var command *develop.BotCommand
		switch r.Method {
		case "GET":
		case "POST":
			if name != "" {
				w.WriteHeader(405)
				return
			}
			action = "create"
			command = &develop.BotCommand{}
			if e := decode(w, r, command); e != nil {
				fail(e)
				return
			}
		case "PUT":
			if name == "" {
				w.WriteHeader(405)
				return
			}
			action = "update"
			command = &develop.BotCommand{}
			if e := decode(w, r, command); e != nil {
				fail(e)
				return
			}
		case "DELETE":
			if name == "" {
				w.WriteHeader(405)
				return
			}
			action = "delete"
			if e := empty(w, r); e != nil {
				fail(e)
				return
			}
		default:
			w.WriteHeader(405)
			return
		}
		v, e := s.Repository.Command(ctx, scope, id, name, command, action)
		if e != nil {
			fail(e)
			return
		}
		if action == "delete" {
			send(w, 204, nil)
		} else {
			send(w, 200, v)
		}
		return
	}
	w.WriteHeader(405)
}
func (s *Service) miniHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, scope apps.Scope, id string, parts []string, offset, take int) {
	fail := func(e error) { failure(w, e, true) }
	if len(parts) == 3 && r.Method == "GET" {
		v, total, e := s.Repository.ListMiniApps(ctx, scope, offset, take)
		if e != nil {
			fail(e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		send(w, 200, v)
		return
	}
	if len(parts) == 5 && parts[4] == "package" {
		if r.Method == "POST" {
			s.uploadPackage(w, r, ctx, scope, id)
			return
		}
		if r.Method == "PUT" {
			var input struct {
				FileID string `json:"file_id"`
			}
			if e := decode(w, r, &input); e != nil {
				fail(e)
				return
			}
			v, e := s.Repository.LinkPackage(ctx, scope, id, input.FileID)
			if e != nil {
				fail(e)
				return
			}
			send(w, 200, v)
			return
		}
		if _, e := s.Repository.MiniApps(ctx, scope, id, nil, "read"); e != nil {
			fail(e)
			return
		}
		fail(ErrUnsupported)
		return
	}
	action := "read"
	var p *MiniPatch
	switch {
	case len(parts) == 3 && r.Method == "POST":
		action = "create"
		p = &MiniPatch{}
	case len(parts) == 4 && r.Method == "PATCH":
		action = "update"
		p = &MiniPatch{}
	case len(parts) == 4 && r.Method == "DELETE":
		action = "delete"
		if e := empty(w, r); e != nil {
			fail(e)
			return
		}
	case len(parts) == 4 && r.Method == "GET":
	default:
		w.WriteHeader(405)
		return
	}
	if p != nil {
		if e := decode(w, r, p); e != nil {
			fail(e)
			return
		}
	}
	v, e := s.Repository.MiniApps(ctx, scope, id, p, action)
	if e != nil {
		fail(e)
		return
	}
	if action == "delete" {
		send(w, 204, nil)
		return
	}
	status := 200
	if action == "create" {
		status = 201
		w.Header().Set("Location", "/api/private/miniapps/"+v[0].ID+"?"+url.Values{"dev": {scope.Developer}, "proj": {scope.Project}, "app": {scope.App}}.Encode())
	}
	send(w, status, v[0])
}
