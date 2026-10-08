// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
)

type Service struct {
	Repository Repository
	Authority  Authority
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == 204 {
		w.WriteHeader(status)
		return
	}
	b, e := json.Marshal(v)
	if e != nil {
		status = 503
		b = []byte(`{"code":"SOCIAL_UNAVAILABLE","message":"Response unavailable.","status":503}`)
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	if errors.As(e, &f) {
		send(w, f.Status, f)
		return
	}
	var a *authority.Failure
	if errors.As(e, &a) {
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="social"`)
		}
		send(w, a.Status, a)
		return
	}
	send(w, 503, ErrUnavailable)
}
func decode(w http.ResponseWriter, r *http.Request, v any, optional bool) error {
	if r.Body == nil {
		if optional {
			return nil
		}
		return ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	var raw json.RawMessage
	e := d.Decode(&raw)
	if e == io.EOF && optional {
		return nil
	}
	if e != nil {
		return ErrInvalid
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if optional {
			return nil
		}
		return ErrInvalid
	}
	var more any
	if d.Decode(&more) != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return ErrInvalid
	}
	return nil
}
func (s *Service) principal(r *http.Request, permissions ...string) (string, error) {
	if s.Authority == nil {
		return "", authority.ErrUnavailable
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		return "", e
	}
	if len(permissions) > 0 {
		p, e = s.Authority.AuthorizePrincipal(r.Context(), p, permissions...)
	} else {
		p, e = s.Authority.AuthenticatePrincipal(r.Context(), p)
	}
	return p.Account.ID, e
}
func page(r *http.Request) (int, int, error) {
	off, take := 0, 20
	var e error
	if s := r.URL.Query().Get("offset"); s != "" {
		off, e = strconv.Atoi(s)
		if e != nil {
			return 0, 0, ErrInvalid
		}
	}
	if s := r.URL.Query().Get("take"); s != "" {
		take, e = strconv.Atoi(s)
		if e != nil {
			return 0, 0, ErrInvalid
		}
	}
	return off, take, ValidatePage(off, take)
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	if r.Method == "HEAD" {
		w.Header().Set("Allow", "GET, POST, PATCH, DELETE")
		w.WriteHeader(405)
		return
	}
	p := r.URL.Path
	if path.Clean(p) != p || strings.Contains(p, "\\") || r.URL.RawPath != "" || r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	hasAuth := false
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			hasAuth = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-realm-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-synthetic-") || strings.HasPrefix(k, "x-test-") {
			failure(w, ErrForbidden)
			return
		}
	}
	for k := range r.URL.Query() {
		if k == "access_token" || k == "token" || k == "tk" {
			failure(w, ErrInvalid)
			return
		}
	}
	// The modular host removes bearer headers after binding verified proof.
	// Consult the authority even without a header so private realm reads retain
	// that identity. Only absent credentials may be treated as anonymous.
	actor, e := s.principal(r)
	if e != nil {
		if hasAuth || !errors.Is(e, authority.ErrInvalid) {
			failure(w, e)
			return
		}
		actor = ""
	}
	// Stargate is an evidenced client route alias only; it is not a legacy source dependency.
	for _, base := range []string{"/api/relationships", "/stargate/relationships"} {
		if p == base || strings.HasPrefix(p, base+"/") {
			tail := strings.TrimPrefix(p, base)
			parts := []string{}
			if tail != "" {
				parts = strings.Split(strings.TrimPrefix(tail, "/"), "/")
			}
			s.relationshipHTTP(w, r, parts)
			return
		}
	}
	if p != "/api/realms" && !strings.HasPrefix(p, "/api/realms/") {
		http.NotFound(w, r)
		return
	}
	tail := strings.TrimPrefix(p, "/api/realms")
	parts := []string{}
	if tail != "" {
		parts = strings.Split(strings.TrimPrefix(tail, "/"), "/")
	}
	s.realmHTTP(w, r, actor, parts)
}
func (s *Service) relationshipHTTP(w http.ResponseWriter, r *http.Request, p []string) {
	actor, e := s.principal(r)
	if e != nil {
		failure(w, e)
		return
	}
	ctx := r.Context()
	if r.Method == "GET" {
		off, take, e := page(r)
		if e != nil {
			failure(w, e)
			return
		}
		switch {
		case len(p) == 0 || len(p) == 1 && p[0] == "requests":
			rows, total, e := s.Repository.Relationships(ctx, actor, len(p) == 1, off, take)
			if e != nil {
				failure(w, e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			send(w, 200, rows)
		case len(p) == 1:
			v, e := s.Repository.Relationship(ctx, actor, p[0])
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		default:
			http.NotFound(w, r)
		}
		return
	}
	if len(p) == 0 || !authority.ValidID(p[0]) {
		failure(w, ErrInvalid)
		return
	}
	c := RelationshipChange{}
	empty := false
	switch {
	case len(p) == 1 && r.Method == "DELETE":
		c.Action = Forget
		empty = true
	case len(p) == 1 && r.Method == "PATCH":
		var body struct {
			Status *int `json:"status"`
		}
		if e = decode(w, r, &body, false); e != nil || body.Status == nil {
			failure(w, ErrInvalid)
			return
		}
		c.Action = SetStatus
		c.Status = *body.Status
	case len(p) == 2 && p[1] == "friends" && r.Method == "POST":
		c.Action = RequestFriend
	case len(p) == 2 && p[1] == "friends" && r.Method == "DELETE":
		c.Action = CancelFriend
		empty = true
	case len(p) == 3 && p[1] == "friends" && p[2] == "accept" && r.Method == "POST":
		c.Action = AcceptFriend
	case len(p) == 3 && p[1] == "friends" && p[2] == "decline" && r.Method == "POST":
		c.Action = DeclineFriend
		empty = true
	case len(p) == 2 && p[1] == "block" && r.Method == "POST":
		var body struct {
			ExpiresIn *string `json:"expires_in"`
			DegradeTo *int    `json:"degrade_to"`
		}
		if e = decode(w, r, &body, true); e != nil {
			failure(w, e)
			return
		}
		if body.ExpiresIn != nil || body.DegradeTo != nil {
			failure(w, ErrUnsupported)
			return
		}
		c.Action = Block
	case len(p) == 2 && p[1] == "close-friend" && (r.Method == "POST" || r.Method == "DELETE"):
		c.Action = SetClose
		if r.Method == "DELETE" {
			c.Action = UnsetClose
		}
	case len(p) == 2 && p[1] == "alias" && r.Method == "PATCH":
		var body struct {
			Alias *string `json:"alias"`
		}
		if e = decode(w, r, &body, false); e != nil {
			failure(w, e)
			return
		}
		c.Action = SetAlias
		c.Alias = body.Alias
	case len(p) == 2 && (p[1] == "mute" || p[1] == "unmute"):
		failure(w, ErrUnsupported)
		return
	default:
		http.NotFound(w, r)
		return
	}
	actor, e = s.principal(r, RelationshipPermissions(c)...)
	if e != nil {
		failure(w, e)
		return
	}
	v, e := s.Repository.ChangeRelationship(ctx, actor, p[0], c)
	if e != nil {
		failure(w, e)
		return
	}
	if empty {
		send(w, 204, nil)
	} else {
		send(w, 200, v)
	}
}
func (s *Service) realmHTTP(w http.ResponseWriter, r *http.Request, actor string, p []string) {
	ctx := r.Context()
	if r.Method == "GET" {
		off, take, e := page(r)
		if e != nil {
			failure(w, e)
			return
		}
		private := len(p) == 0 || len(p) == 1 && p[0] == "invites" || len(p) == 3 && p[1] == "members" && p[2] == "me"
		if private {
			actor, e = s.principal(r)
			if e != nil {
				failure(w, e)
				return
			}
		}
		switch {
		case len(p) == 0:
			v, e := s.Repository.ListRealms(ctx, actor, off, take)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		case len(p) == 1 && p[0] == "invites":
			v, e := s.Repository.Invites(ctx, actor, off, take)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		case len(p) == 1 && p[0] == "quota":
			failure(w, ErrUnsupported)
		case len(p) == 1:
			v, e := s.Repository.GetRealm(ctx, actor, p[0])
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		case len(p) == 2 && p[1] == "members":
			for _, key := range []string{"withStatus", "with_status", "accountName", "account_name", "labelId", "label_id"} {
				if r.URL.Query().Get(key) != "" {
					failure(w, ErrUnsupported)
					return
				}
			}
			v, total, e := s.Repository.Members(ctx, actor, p[0], off, take)
			if e != nil {
				failure(w, e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			send(w, 200, v)
		case len(p) == 3 && p[1] == "members" && p[2] == "me":
			v, e := s.Repository.Me(ctx, actor, p[0])
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		default:
			failure(w, ErrUnsupported)
		}
		return
	}
	permission := "realms.members.manage"
	if len(p) == 0 {
		permission = "realms.create"
	} else if p[0] == "invites" {
		permission = "realms.invites.manage"
	} else if len(p) == 1 {
		if r.Method == "DELETE" {
			permission = "realms.delete"
		} else {
			permission = "realms.update"
		}
	}
	var e error
	actor, e = s.principal(r, permission)
	if e != nil {
		failure(w, e)
		return
	}
	if len(p) == 0 && r.Method == "POST" || len(p) == 1 && r.Method == "PATCH" {
		var patch RealmPatch
		if e = decode(w, r, &patch, false); e != nil {
			failure(w, e)
			return
		}
		var v Realm
		if len(p) == 0 {
			v, e = s.Repository.CreateRealm(ctx, actor, patch)
		} else {
			v, e = s.Repository.UpdateRealm(ctx, actor, p[0], patch)
		}
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, v)
		return
	}
	if len(p) == 1 && r.Method == "DELETE" {
		if e = s.Repository.DeleteRealm(ctx, actor, p[0]); e != nil {
			failure(w, e)
			return
		}
		send(w, 204, nil)
		return
	}
	c := MemberChange{}
	ref := ""
	empty := false
	switch {
	case len(p) == 2 && p[0] == "invites" && r.Method == "POST":
		var body InviteRequest
		if e = decode(w, r, &body, false); e != nil {
			failure(w, e)
			return
		}
		ref = p[1]
		c = MemberChange{Action: Invite, AccountID: body.RelatedUserID, Role: body.Role}
	case len(p) == 3 && p[0] == "invites" && p[2] == "accept" && r.Method == "POST":
		ref = p[1]
		c.Action = Accept
	case len(p) == 3 && p[0] == "invites" && p[2] == "decline" && r.Method == "POST":
		ref = p[1]
		c.Action = Decline
		empty = true
	case len(p) == 3 && p[1] == "members" && p[2] == "me" && r.Method == "POST":
		ref = p[0]
		c.Action = Join
	case len(p) == 3 && p[1] == "members" && p[2] == "me" && r.Method == "DELETE":
		ref = p[0]
		c.Action = Leave
		empty = true
	case len(p) == 3 && p[1] == "members" && r.Method == "DELETE":
		ref = p[0]
		c.Action = Remove
		c.AccountID = p[2]
		empty = true
	case len(p) == 4 && p[1] == "members" && p[3] == "role" && r.Method == "PATCH":
		var role Role
		if e = decode(w, r, &role, false); e != nil {
			failure(w, e)
			return
		}
		ref = p[0]
		c = MemberChange{Action: ChangeRole, AccountID: p[2], Role: role}
	default:
		failure(w, ErrUnsupported)
		return
	}
	v, e := s.Repository.ChangeMember(ctx, actor, ref, c)
	if e != nil {
		failure(w, e)
		return
	}
	if empty {
		send(w, 204, nil)
	} else {
		send(w, 200, v)
	}
}
