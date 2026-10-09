// SPDX-License-Identifier: AGPL-3.0-only
package rooms

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
	"solsynth.local/go-migration/ports/messager"
	"strconv"
	"strings"
	"unicode/utf8"
)

const ProjectionVersion = "owned-text-room-v1"

type Service struct {
	Repository   Repository
	Organization OrganizationRepository
	Authority    Authority
	Reader       messager.SyncReader
	Bind         func(context.Context, authority.Principal) context.Context
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Solsynth-Projection", ProjectionVersion)
	if status == 204 {
		w.WriteHeader(status)
		return
	}
	b, e := json.Marshal(v)
	if e != nil {
		status = 503
		b = []byte(`{"code":"CHAT_UNAVAILABLE"}`)
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	switch {
	case errors.As(e, &f):
		send(w, f.Status, f)
	case errors.As(e, &a):
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="chat"`)
		}
		send(w, a.Status, a)
	default:
		send(w, 503, ErrUnavailable)
	}
}
func (s *Service) principal(r *http.Request, permission string) (authority.Principal, error) {
	if s == nil || s.Authority == nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		return p, e
	}
	if permission == "" {
		return s.Authority.AuthenticatePrincipal(r.Context(), p)
	}
	return s.Authority.AuthorizePrincipal(r.Context(), p, permission)
}

// Reuse the existing message read/sync/unread handler rather than introducing a
// second membership/message reader. The host may supply a proof-only adapter.
type readIdentity struct{ service *Service }

func (a readIdentity) Account(r *http.Request) (string, error) {
	if a.service == nil || a.service.Authority == nil {
		return "", messager.ErrSyncUnavailable
	}
	p, e := a.service.Authority.AuthenticateRequest(r)
	// Only the absence of an initial identity may become an anonymous read.
	// A supplied proof failing its fresh-state check must never be downgraded,
	// including when the embedding host has already stripped bearer headers.
	if e != nil && errors.Is(e, authority.ErrInvalid) && r.Method == "GET" && strings.Contains(r.URL.Path, "/messages") && len(r.Header.Values("Authorization")) == 0 {
		return "", nil
	}
	if e == nil {
		p, e = a.service.Authority.AuthenticatePrincipal(r.Context(), p)
	}
	if e != nil {
		var f *authority.Failure
		if errors.As(e, &f) {
			if f.Status == 401 {
				return "", messager.ErrSyncUnauthorized
			}
			if f.Status == 403 || f.Status == 423 {
				return "", messager.ErrSyncForbidden
			}
		}
		return "", messager.ErrSyncUnavailable
	}
	if !ValidID(p.Account.ID) {
		return "", messager.ErrSyncUnauthorized
	}
	return p.Account.ID, nil
}

func isReadRoute(r *http.Request, p []string) bool {
	return r.Method == "GET" && (len(p) == 1 && p[0] == "unread" || (len(p) == 2 || len(p) == 3) && p[1] == "messages") || r.Method == "POST" && len(p) == 2 && p[1] == "sync"
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != r.URL.Path {
		failure(w, ErrInvalid)
		return
	}
	for k := range r.Header {
		key := strings.ToLower(k)
		if key == "x-account-id" || key == "x-user-id" || key == "x-role" || key == "x-app-secret" || key == "proxy-authorization" || strings.HasPrefix(key, "x-test-") || strings.HasPrefix(key, "x-synthetic-") {
			failure(w, ErrForbidden)
			return
		}
	}
	if r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	for key := range r.URL.Query() {
		if key == "token" || key == "tk" || key == "access_token" {
			failure(w, ErrInvalid)
			return
		}
		if key == "identity" {
			failure(w, ErrUnsupported)
			return
		}
	}
	if s != nil && s.serveMLS(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/realms/") {
		s.serveRealm(w, r)
		return
	}
	if r.URL.Path != "/api/chat" && !strings.HasPrefix(r.URL.Path, "/api/chat/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/chat")
	p := []string{}
	if rest != "" {
		p = strings.Split(strings.TrimPrefix(rest, "/"), "/")
	}
	if s.serveSubscriptionSync(w, r, p) {
		return
	}
	if s.serveDetails(w, r, p) {
		return
	}
	if s.serveOrganization(w, r, p) {
		return
	}
	if len(p) == 4 && p[1] == "messages" && p[3] == "thread" {
		s.serveThread(w, r, p)
		return
	}
	if len(p) >= 4 && p[1] == "messages" && p[3] == "reactions" {
		s.serveReactions(w, r, p)
		return
	}
	if s.serveTransient(w, r, p) {
		return
	}
	if isReadRoute(r, p) {
		if s == nil || s.Reader == nil {
			failure(w, ErrUnavailable)
			return
		}
		messager.NewAuthoritySyncHandler(s.Reader, readIdentity{s}).ServeHTTP(w, r)
		return
	}
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	permission := ""
	switch {
	case len(p) == 0 && r.Method == "POST":
		permission = "chat.create"
	case len(p) == 1 && p[0] == "direct" && r.Method == "POST":
		permission = "chat.create"
	case len(p) == 4 && p[1] == "members" && p[3] == "timeout":
		permission = "chat.members.timeout"
	case len(p) == 3 && p[1] == "media" && r.Method == "DELETE":
		permission = "chat.update"
	case len(p) == 1 && r.Method == "PATCH":
		permission = "chat.update"
	case len(p) == 1 && r.Method == "DELETE":
		permission = "chat.delete"
	case len(p) == 2 && p[0] == "invites" && r.Method == "POST":
		permission = "chat.invites.manage"
	case len(p) == 3 && p[1] == "members" && p[2] != "me" && r.Method == "DELETE":
		permission = "chat.members.kick"
	case (len(p) == 2 || len(p) == 3 && p[2] == "redirect") && p[1] == "messages" && r.Method == "POST":
		permission = "chat.messages.create"
	case len(p) == 3 && p[1] == "messages" && r.Method == "PATCH":
		permission = "chat.messages.update"
	case len(p) >= 4 && p[1] == "messages" && p[3] == "reactions" && (r.Method == "POST" || r.Method == "DELETE"):
		permission = "chat.messages.react"
	case len(p) == 3 && p[1] == "messages" && r.Method == "DELETE":
		permission = "chat.messages.delete"
	}
	principal, e := s.principal(r, permission)
	if e != nil {
		failure(w, e)
		return
	}
	actor := principal.Account.ID
	if !ValidID(actor) {
		failure(w, authority.ErrInvalid)
		return
	}
	if r.Method == "HEAD" {
		w.Header().Set("Allow", "GET, POST, PATCH, DELETE")
		send(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
		return
	}
	if r.URL.RawQuery != "" && !(r.Method == "GET" && (len(p) == 0 || len(p) == 1 && p[0] == "invites" || len(p) == 2 && p[1] == "members")) {
		failure(w, ErrInvalid)
		return
	}
	for k, v := range r.URL.Query() {
		if (k != "take" && k != "offset") || len(v) != 1 {
			failure(w, ErrInvalid)
			return
		}
	}
	take, offset := 100, 0
	for key, target := range map[string]*int{"take": &take, "offset": &offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil {
				failure(w, ErrInvalid)
				return
			}
			*target = n
		}
	}
	if take < 1 || take > 100 || offset < 0 || offset > 100000 {
		failure(w, ErrInvalid)
		return
	}
	ctx := r.Context()
	if s.Bind != nil {
		ctx = s.Bind(ctx, principal)
	}
	writeProof, e := mlsWriteContext(r)
	if e != nil {
		failure(w, e)
		return
	}
	ctx = WithMLSWrite(ctx, writeProof)
	var result any
	status := 200
	switch {
	case len(p) >= 1 && p[0] == "direct":
		relations, ok := s.Repository.(RelationRepository)
		if !ok {
			failure(w, ErrUnsupported)
			return
		}
		if len(p) == 1 && r.Method == "POST" {
			var req DirectRequest
			e = decode(w, r, &req, "related_user_id", "encryption_mode")
			if e == nil {
				result, e = relations.CreateDirect(ctx, actor, req)
			}
		} else if len(p) == 2 && r.Method == "GET" && ValidID(p[1]) {
			result, e = relations.GetDirect(ctx, actor, p[1])
		} else {
			method(w, "GET, POST")
			return
		}
	case len(p) == 0:
		switch r.Method {
		case "GET":
			var list []Room
			list, e = s.Repository.List(ctx, actor, take, offset)
			result = map[string]any{"rooms": list}
		case "POST":
			var req RoomPatch
			e = decode(w, r, &req, "name", "description", "is_public", "is_community", "is_read_receipts_public", "encryption_mode", "realm_id", "picture_id", "background_id", "slug")
			if e == nil {
				result, e = s.Repository.Create(ctx, actor, req)
			}
		default:
			method(w, "GET, POST")
			return
		}
	case len(p) == 1 && p[0] == "invites":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		result, e = s.Repository.Invites(ctx, actor, take, offset)
	case len(p) >= 2 && p[0] == "invites":
		if !ValidID(p[1]) {
			failure(w, ErrInvalid)
			return
		}
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		if len(p) == 2 {
			var req struct {
				RelatedUserID string `json:"related_user_id"`
				Role          *int   `json:"role"`
			}
			e = decode(w, r, &req, "related_user_id", "role")
			if e == nil && req.Role != nil && *req.Role != 0 {
				e = ErrUnsupported
			}
			if e == nil {
				result, e = s.Repository.ChangeMember(ctx, actor, p[1], Invite, req.RelatedUserID)
			}
		} else if len(p) == 3 && (p[2] == "accept" || p[2] == "decline") {
			e = empty(w, r)
			if e == nil {
				result, e = s.Repository.ChangeMember(ctx, actor, p[1], MemberAction(p[2]), "")
			}
			if p[2] == "decline" {
				status = 204
			}
		} else {
			e = ErrUnsupported
		}
	case len(p) > 0 && ValidID(p[0]):
		rid := p[0]
		switch {
		case len(p) == 2 && (p[1] == "confirm" || p[1] == "block"):
			relations, ok := s.Repository.(RelationRepository)
			if !ok {
				failure(w, ErrUnsupported)
				return
			}
			action := p[1]
			if p[1] == "block" && r.Method == "DELETE" {
				action = "unblock"
			} else if r.Method != "POST" {
				method(w, "POST, DELETE")
				return
			}
			e = empty(w, r)
			if e == nil {
				var room Room
				room, e = relations.ChangeDirect(ctx, actor, rid, action)
				result = room
				if action == "block" {
					status = 204
				}
				if action == "unblock" && e == nil {
					for _, member := range room.Members {
						if member.AccountID == actor {
							result = member
							break
						}
					}
				}
			}
		case len(p) == 4 && p[1] == "members" && ValidID(p[2]) && p[3] == "timeout":
			relations, ok := s.Repository.(RelationRepository)
			if !ok {
				failure(w, ErrUnsupported)
				return
			}
			var req *TimeoutRequest
			if r.Method == "POST" {
				req = &TimeoutRequest{}
				e = decode(w, r, req, "reason", "timeout_until")
			} else if r.Method == "DELETE" {
				e = empty(w, r)
			} else {
				method(w, "POST, DELETE")
				return
			}
			if e == nil {
				e = relations.Timeout(ctx, actor, rid, p[2], req)
			}
			status = 204
		case len(p) == 3 && p[1] == "media" && (p[2] == "picture" || p[2] == "background"):
			if r.Method != "DELETE" {
				method(w, "DELETE")
				return
			}
			e = empty(w, r)
			if e == nil {
				_, e = s.Repository.Update(ctx, actor, rid, RoomPatch{RemovePicture: p[2] == "picture", RemoveBackground: p[2] == "background"})
			}
			status = 204
		case len(p) == 1:
			switch r.Method {
			case "GET":
				result, e = s.Repository.Get(ctx, actor, rid)
			case "PATCH":
				var req RoomPatch
				e = decode(w, r, &req, "name", "description", "is_public", "is_community", "is_read_receipts_public", "encryption_mode", "realm_id", "picture_id", "background_id", "slug")
				if e == nil {
					result, e = s.Repository.Update(ctx, actor, rid, req)
				}
			case "DELETE":
				e = empty(w, r)
				if e == nil {
					e = s.Repository.Delete(ctx, actor, rid)
				}
				status = 204
			default:
				method(w, "GET, PATCH, DELETE")
				return
			}
		case len(p) == 2 && p[1] == "members":
			if r.Method != "GET" {
				method(w, "GET")
				return
			}
			result, e = s.Repository.Members(ctx, actor, rid, take, offset)
		case len(p) == 3 && p[1] == "members" && p[2] == "me":
			switch r.Method {
			case "GET":
				if s.Organization != nil {
					result, e = s.Organization.GroupMember(ctx, actor, rid)
				} else {
					result, e = s.Repository.Me(ctx, actor, rid)
				}
			case "POST":
				e = empty(w, r)
				if e == nil {
					result, e = s.Repository.ChangeMember(ctx, actor, rid, Join, "")
				}
			case "DELETE":
				e = empty(w, r)
				if e == nil {
					result, e = s.Repository.ChangeMember(ctx, actor, rid, Leave, "")
				}
				status = 204
			default:
				method(w, "GET, POST, DELETE")
				return
			}
		case len(p) == 3 && p[1] == "members" && ValidID(p[2]):
			if r.Method != "DELETE" {
				method(w, "DELETE")
				return
			}
			e = empty(w, r)
			if e == nil {
				result, e = s.Repository.ChangeMember(ctx, actor, rid, Remove, p[2])
			}
			status = 204
		case len(p) == 4 && p[1] == "members" && p[2] == "me" && p[3] == "notify":
			if r.Method != "PATCH" {
				method(w, "PATCH")
				return
			}
			var req struct {
				Notify *int32 `json:"notify_level"`
			}
			e = decode(w, r, &req, "notify_level")
			if e == nil {
				if req.Notify == nil {
					e = ErrInvalid
				} else {
					result, e = s.Repository.SetNotify(ctx, actor, rid, *req.Notify)
				}
			}
		case len(p) == 4 && p[1] == "members" && p[2] == "me" && p[3] == "read":
			if r.Method != "POST" {
				method(w, "POST")
				return
			}
			var req struct {
				MessageID string `json:"message_id"`
			}
			e = decode(w, r, &req, "message_id")
			if e == nil {
				result, e = s.Repository.MarkRead(ctx, actor, rid, req.MessageID)
			}
		case len(p) == 3 && p[1] == "messages" && p[2] == "redirect":
			if r.Method != "POST" {
				method(w, "POST")
				return
			}
			repo, ok := s.Repository.(ThreadRepository)
			if !ok {
				e = ErrUnsupported
				break
			}
			if len(r.Header.Values("Idempotency-Key")) > 1 {
				e = ErrInvalid
				break
			}
			var request RedirectRequest
			e = decode(w, r, &request, "message_ids")
			if e == nil {
				result, e = repo.Redirect(ctx, actor, rid, request.MessageIDs, r.Header.Get("Idempotency-Key"))
			}
		case len(p) == 2 && p[1] == "messages":
			if r.Method != "POST" {
				method(w, "GET, POST")
				return
			}
			var req TextRequest
			e = s.decodeText(w, r, &req)
			if e == nil {
				req, e = ValidateText(req, false)
			}
			if e == nil {
				result, e = s.Repository.Send(ctx, actor, rid, req)
			}
		case len(p) == 3 && p[1] == "messages" && ValidID(p[2]):
			switch r.Method {
			case "PATCH":
				var req TextRequest
				e = s.decodeText(w, r, &req)
				if e == nil {
					req, e = ValidateText(req, true)
				}
				if e == nil {
					result, e = s.Repository.Edit(ctx, actor, rid, p[2], req)
				}
			case "DELETE":
				var deletion TextRequest
				deletion, e = s.decodeMLSDelete(w, r)
				if e == nil {
					proof := MLSWriteFrom(ctx)
					proof.Encryption = deletion.EncryptionMeta
					proof.ClientMessageID = deletion.ClientMessageID
					ctx = WithMLSWrite(ctx, proof)
					e = s.Repository.DeleteMessage(ctx, actor, rid, p[2])
				}
				status = 204
			default:
				method(w, "GET, PATCH, DELETE")
				return
			}
		default:
			e = ErrUnsupported
		}
	default:
		e = ErrUnsupported
	}
	if e != nil {
		failure(w, e)
		return
	}
	if m, ok := result.(messager.MessageProjection); ok {
		m.Content = messager.FormatMessageContent(m.Content, len(m.EncryptionMeta) > 0 && string(m.EncryptionMeta) != "null")
		result = m
	}
	send(w, status, result)
}
func method(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	send(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
}

var unsupported = map[string]bool{"realm_id": true, "picture_id": true, "background_id": true, "slug": true, "e2ee_policy": true, "role": true, "encryption_meta": true, "attachments_id": true, "embeds": true, "meta": true, "fund_id": true, "survey_id": true, "meet_id": true, "notable_day_id": true, "calendar_event_id": true, "location_name": true, "location_address": true, "location_wkt": true, "forwarded_message_id": true, "thread_id": true, "is_thread_root": true, "break_until": true}

// Exact, duplicate-free top-level names avoid JSON's last-key-wins ambiguity.
// Unsupported provider/encryption fields fail explicitly, including empty ones.
func decode(w http.ResponseWriter, r *http.Request, out any, allowed ...string) error {
	if raw := r.Header.Get("Content-Type"); raw != "" {
		kind, _, e := mime.ParseMediaType(raw)
		if e != nil || kind != "application/json" {
			return ErrInvalid
		}
	}
	if r.Body == nil {
		return ErrInvalid
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if e != nil || !utf8.Valid(data) {
		return ErrInvalid
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
		if !ok || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
		if !keys[name] {
			if unsupported[name] {
				return ErrUnsupported
			}
			return ErrInvalid
		}
		var value json.RawMessage
		if e = d.Decode(&value); e != nil {
			return ErrInvalid
		}
	}
	if _, e = d.Token(); e != nil {
		return ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	if e = json.Unmarshal(data, out); e != nil {
		return ErrInvalid
	}
	return nil
}
func empty(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if e != nil {
		return ErrInvalid
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	var out struct{}
	return decode(w, r, &out)
}
