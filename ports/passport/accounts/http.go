// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Service struct {
	Repository Repository
	Authority  Authority
}

// Handler accepts only authority-verified bearer proof. It does not trust proxy
// identity headers or a hand-constructed principal from a caller.
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func send(w http.ResponseWriter, status int, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		failure(w, ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
func failure(w http.ResponseWriter, e error) {
	e = NormalizeError(e)
	var f *Failure
	if errors.As(e, &f) {
		send(w, f.Status, f)
		return
	}
	var a *authority.Failure
	if errors.As(e, &a) {
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="accounts"`)
		}
		send(w, a.Status, a)
		return
	}
	send(w, 503, ErrUnavailable)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if e := d.Decode(&raw); e != nil || !utf8.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalid
	}
	var more any
	if e := d.Decode(&more); e != io.EOF {
		return ErrInvalid
	}
	if e := uniqueProfileJSON(json.NewDecoder(bytes.NewReader(raw)), 0); e != nil {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return ErrInvalid
	}
	if p, ok := v.(*ProfilePatch); ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return ErrInvalid
		}
		_, p.BirthdayPresent = fields["birthday"]
	}
	return nil
}
func (s *Service) principal(r *http.Request, permissions ...string) (authority.Principal, error) {
	if s.Authority == nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		return p, e
	}
	if len(permissions) == 0 {
		return s.Authority.AuthenticatePrincipal(r.Context(), p)
	}
	return s.Authority.AuthorizePrincipal(r.Context(), p, permissions...)
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.Header().Set("Allow", "GET, POST, PATCH, DELETE")
		w.WriteHeader(405)
		return
	}
	authPresent := false
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			authPresent = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "x-publisher-role" || strings.HasPrefix(k, "x-synthetic-") || strings.HasPrefix(k, "x-test-") || k == "proxy-authorization" {
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
	if r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	// Invalid supplied credentials never silently downgrade to anonymous access.
	if authPresent {
		if _, e := s.principal(r); e != nil {
			failure(w, e)
			return
		}
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// The read aliases are evidenced by the authored Solian client. Updates use
	// the separately documented owned endpoint only.
	if r.Method == "GET" && (path == "/stargate/accounts/me" || path == "/api/accounts/me") {
		p, e := s.principal(r)
		if e != nil {
			failure(w, e)
			return
		}
		ctx := accountContext(s.Repository, r.Context(), p)
		var a Account
		if own, ok := s.Repository.(interface {
			LookupSelf(context.Context, string) (Account, error)
		}); ok {
			a, e = own.LookupSelf(ctx, p.Account.ID)
		} else {
			a, e = s.Repository.LookupAccount(ctx, p.Account.ID)
		}
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, a)
		return
	}
	if r.Method == "GET" && len(parts) == 3 && (parts[0] == "api" || parts[0] == "stargate") && parts[1] == "accounts" && parts[2] != "me" {
		a, e := s.Repository.LookupAccount(r.Context(), parts[2])
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, a)
		return
	}
	if (path == "/api/accounts/me" || path == "/stargate/accounts/me" || path == "/stargate/accounts/me/profile" || path == "/api/accounts/me/profile") && r.Method == "PATCH" {
		p, e := s.principal(r, "accounts.manage")
		if e != nil {
			failure(w, e)
			return
		}
		var patch ProfilePatch
		if e = decode(w, r, &patch); e != nil {
			failure(w, e)
			return
		}
		if strings.HasSuffix(path, "/profile") && (patch.Name != nil || patch.Nick != nil || patch.Language != nil || patch.Region != nil) {
			failure(w, ErrInvalid)
			return
		}
		if patch.Name != nil {
			if *patch.Name != p.Account.Name {
				failure(w, ErrNameChange)
				return
			}
			patch.Name = nil
		}
		if e = ValidateProfile(patch); e != nil {
			failure(w, e)
			return
		}
		a, e := s.Repository.UpdateProfile(accountContext(s.Repository, r.Context(), p), p.Account.ID, patch)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, a)
		return
	}
	if strings.HasPrefix(path, "/api/accounts/me/name-change-card") {
		if _, e := s.principal(r); e != nil {
			failure(w, e)
			return
		}
		failure(w, ErrUnsupported)
		return
	}
	if len(parts) < 2 || parts[0] != "api" || parts[1] != "publishers" {
		http.NotFound(w, r)
		return
	}
	pparts := parts[2:]
	if r.Method == "GET" {
		s.publisherRead(w, r, pparts)
		return
	}
	s.publisherWrite(w, r, pparts)
}
func (s *Service) publisherRead(w http.ResponseWriter, r *http.Request, p []string) {
	ctx := r.Context()
	viewer, e := s.publisherViewer(r)
	if e != nil {
		failure(w, e)
		return
	}
	switch {
	case len(p) == 0:
		actor, e := s.principal(r)
		if e != nil {
			failure(w, e)
			return
		}
		out, e := s.listPublishers(ctx, actor.Account.ID, actor.Account.ID)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
	case len(p) == 1 && p[0] == "invites":
		actor, e := s.principal(r)
		if e != nil {
			failure(w, e)
			return
		}
		out, e := s.Repository.ListInvites(ctx, actor.Account.ID)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
	case len(p) == 2 && p[0] == "of":
		out, e := s.listPublishers(ctx, viewer, p[1])
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
	case len(p) == 1 && p[0] != "quota" && p[0] != "search" && p[0] != "leaderboard":
		out, e := s.lookupPublisher(ctx, viewer, p[0])
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
	case len(p) == 2 && p[1] == "members":
		off, take := 0, 20
		var e error
		if v := r.URL.Query().Get("offset"); v != "" {
			off, e = strconv.Atoi(v)
		}
		if e == nil {
			if v := r.URL.Query().Get("take"); v != "" {
				take, e = strconv.Atoi(v)
			}
		}
		if e != nil || off < 0 || take < 1 || take > 100 {
			failure(w, ErrInvalid)
			return
		}
		out, total, e := s.listPublisherMembers(ctx, viewer, p[0], off, take)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		send(w, 200, out)
	case len(p) == 3 && p[1] == "members" && p[2] == "me":
		actor, e := s.principal(r)
		if e != nil {
			failure(w, e)
			return
		}
		pub, e := s.lookupPublisher(ctx, viewer, p[0])
		if e != nil {
			failure(w, e)
			return
		}
		out, e := s.Repository.Membership(ctx, pub.ID, actor.Account.ID)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
	default:
		failure(w, ErrUnsupported)
	}
}
func (s *Service) publisherWrite(w http.ResponseWriter, r *http.Request, p []string) {
	var permission string
	var change MemberChange
	var operation string
	switch {
	case len(p) == 2 && p[0] == "organization" && r.Method == "POST":
		permission = "publishers.create"
		operation = "organization"
	case len(p) == 1 && p[0] == "individual" && r.Method == "POST":
		permission = "publishers.create"
		operation = "create"
	case len(p) == 1 && r.Method == "PATCH":
		permission = "publishers.update"
		operation = "update"
	case len(p) == 1 && r.Method == "DELETE":
		permission = "publishers.delete"
		operation = "delete"
	case len(p) == 2 && p[0] == "invites" && r.Method == "POST":
		permission = "publishers.invites.manage"
		change.Action = InviteMember
		operation = "member"
	case len(p) == 3 && p[0] == "invites" && r.Method == "POST" && (p[2] == "accept" || p[2] == "decline"):
		permission = "publishers.invites.manage"
		change.Action = Mutation(p[2])
		operation = "member"
	case len(p) == 3 && p[1] == "members" && r.Method == "DELETE":
		permission = "publishers.members.manage"
		change.Action = RemoveMember
		change.AccountID = p[2]
		if p[2] == "me" {
			change.Action = LeavePublisher
			permission = ""
		}
		operation = "member"
	case len(p) == 4 && p[1] == "members" && p[3] == "role" && r.Method == "PATCH":
		permission = "publishers.members.manage"
		change.Action = UpdateRole
		change.AccountID = p[2]
		operation = "member"
	default:
		if _, e := s.principal(r); e != nil {
			failure(w, e)
			return
		}
		failure(w, ErrUnsupported)
		return
	}
	var perms []string
	if permission != "" {
		perms = []string{permission}
	}
	actor, e := s.principal(r, perms...)
	if e != nil {
		failure(w, e)
		return
	}
	ctx := r.Context()
	switch operation {
	case "create", "organization", "update":
		var patch PublisherPatch
		if e = decode(w, r, &patch); e != nil {
			failure(w, e)
			return
		}
		if e = ValidatePublisher(patch, operation != "update"); e != nil {
			failure(w, e)
			return
		}
		var out Publisher
		if operation == "organization" {
			organization, ok := s.Repository.(OrganizationPublisherRepository)
			if !ok {
				failure(w, ErrUnsupported)
				return
			}
			out, e = organization.CreateOrganizationPublisher(ctx, actor.Account.ID, p[1], patch)
		} else if operation == "create" {
			out, e = s.Repository.CreatePublisher(ctx, actor.Account.ID, patch)
		} else {
			out, e = s.Repository.UpdatePublisher(ctx, actor.Account.ID, p[0], patch)
		}
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
	case "delete":
		if e = s.Repository.DeletePublisher(ctx, actor.Account.ID, p[0]); e != nil {
			failure(w, e)
			return
		}
		w.WriteHeader(204)
	case "member":
		ref := p[0]
		if p[0] == "invites" {
			ref = p[1]
		}
		if change.Action == InviteMember {
			var in Invite
			if e = decode(w, r, &in); e != nil {
				failure(w, e)
				return
			}
			change.AccountID = in.RelatedUserID
			change.Role = in.Role
		} else if change.Action == UpdateRole {
			if e = decode(w, r, &change.Role); e != nil {
				failure(w, e)
				return
			}
		}
		out, e := s.Repository.ChangeMember(ctx, actor.Account.ID, ref, change)
		if e != nil {
			failure(w, e)
			return
		}
		switch change.Action {
		case DeclineInvite, RemoveMember, LeavePublisher:
			w.WriteHeader(204)
		case AcceptInvite:
			pub, e := s.lookupPublisher(ctx, actor.Account.ID, out.PublisherID)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, pub)
		default:
			send(w, 200, out)
		}
	}
}

// EncodeResponse is usable by aggregate hosts after their own proof-based
// authorization; all Account values are already restricted public projections.
func EncodeResponse(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b).Encode(v)
	return b.Bytes(), e
}
