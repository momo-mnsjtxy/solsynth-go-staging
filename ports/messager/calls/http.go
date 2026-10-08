// SPDX-License-Identifier: AGPL-3.0-only
package calls

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"unicode/utf8"
)

const ProjectionVersion = "owned-room-call-v1"

type Service struct {
	Repository Repository
	Authority  Authority
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func ValidTool(tool string) bool {
	if len(tool) > 32 {
		return false
	}
	for _, c := range tool {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Solsynth-Projection", ProjectionVersion)
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	switch {
	case errors.As(e, &f):
		send(w, f.Status, f)
	case errors.As(e, &a):
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="calls"`)
		}
		send(w, a.Status, a)
	default:
		send(w, 503, ErrUnavailable)
	}
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != r.URL.Path || r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	for name := range r.Header {
		k := strings.ToLower(name)
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-app-secret" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, ErrForbidden)
			return
		}
	}
	if !strings.HasPrefix(r.URL.Path, "/api/chat/realtime/") {
		http.NotFound(w, r)
		return
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/chat/realtime/"), "/")
	if len(p) < 1 || len(p) > 3 || !authority.ValidID(p[0]) {
		failure(w, ErrInvalid)
		return
	}
	permission := ""
	operation := ""
	allow := ""
	switch {
	case len(p) == 1:
		allow = "GET, POST, DELETE"
		switch r.Method {
		case "GET":
			operation = "get"
		case "POST":
			operation = "start"
		case "DELETE":
			operation = "leave"
		}
	case len(p) == 2 && (p[1] == "join" || p[1] == "participants"):
		allow = "GET"
		if r.Method == "GET" {
			operation = p[1]
		}
	case len(p) == 2 && p[1] == "end":
		allow = "POST"
		if r.Method == "POST" {
			operation = "end"
		}
	case len(p) == 3 && authority.ValidID(p[2]):
		allow = "POST"
		if r.Method == "POST" {
			switch p[1] {
			case "kick":
				operation = "kick"
				permission = "chat.call.kick"
			case "mute", "unmute":
				operation = p[1]
				permission = "chat.call.mute"
			case "invite":
				operation = "invite"
				permission = "chat.call.invite"
			}
		}
	}
	if allow == "" {
		http.NotFound(w, r)
		return
	}
	if operation == "" {
		w.Header().Set("Allow", allow)
		send(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	for k, v := range q {
		if k != "tool" || len(v) != 1 || (operation != "join" && operation != "leave") {
			failure(w, ErrInvalid)
			return
		}
	}
	tool := q.Get("tool")
	if !ValidTool(tool) {
		failure(w, ErrInvalid)
		return
	}
	if s == nil || s.Repository == nil || s.Authority == nil {
		failure(w, ErrUnconfigured)
		return
	}
	principal, e := s.Authority.AuthenticateRequest(r)
	if e == nil {
		if permission == "" {
			principal, e = s.Authority.AuthenticatePrincipal(r.Context(), principal)
		} else {
			principal, e = s.Authority.AuthorizePrincipal(r.Context(), principal, permission)
		}
	}
	if e != nil {
		failure(w, e)
		return
	}
	actor := principal.Account.ID
	var kick KickRequest
	if operation == "kick" {
		e = decode(w, r, &kick, "ban_duration_minutes", "reason")
	} else {
		e = decode(w, r, &struct{}{})
	}
	if e != nil {
		failure(w, e)
		return
	}
	var result any
	status := 200
	ctx := r.Context()
	rid := p[0]
	switch operation {
	case "get":
		result, e = s.Repository.Get(ctx, actor, rid)
	case "start":
		result, e = s.Repository.Start(ctx, actor, rid)
	case "join":
		result, e = s.Repository.Join(ctx, actor, rid, tool)
	case "participants":
		result, e = s.Repository.Participants(ctx, actor, rid)
	case "leave":
		e = s.Repository.Leave(ctx, actor, rid, tool)
		status = 204
	case "end":
		e = s.Repository.End(ctx, actor, rid)
		status = 204
	case "kick":
		e = s.Repository.Kick(ctx, actor, rid, p[2], kick)
		status = 204
	case "mute", "unmute":
		e = s.Repository.Mute(ctx, actor, rid, p[2], operation == "mute")
		status = 204
	case "invite":
		e = s.Repository.Invite(ctx, actor, rid, p[2])
		status = 204
	}
	if e != nil {
		failure(w, e)
		return
	}
	send(w, status, result)
}
func decode(w http.ResponseWriter, r *http.Request, out any, allowed ...string) error {
	if raw := r.Header.Get("Content-Type"); raw != "" {
		kind, _, e := mime.ParseMediaType(raw)
		if e != nil || kind != "application/json" {
			return ErrInvalid
		}
	}
	if r.Body == nil {
		return nil
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if e != nil || !utf8.Valid(data) {
		return ErrInvalid
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(data))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return ErrInvalid
	}
	keys := map[string]bool{}
	for _, key := range allowed {
		keys[key] = true
	}
	seen := map[string]bool{}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return ErrInvalid
		}
		name, ok := key.(string)
		if !ok || !keys[name] || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return ErrInvalid
		}
	}
	if _, e = d.Token(); e != nil {
		return ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	if json.Unmarshal(data, out) != nil {
		return ErrInvalid
	}
	return nil
}
