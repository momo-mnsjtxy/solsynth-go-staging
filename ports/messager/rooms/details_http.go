// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"errors"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
)

func (s *Service) detailIdentity(r *http.Request, permission string, optional bool) (context.Context, string, error) {
	if s == nil || s.Authority == nil {
		return r.Context(), "", authority.ErrUnavailable
	}
	present := len(r.Header.Values("Authorization")) > 0
	if a, ok := s.Authority.(interface{ RequestIdentityPresent(*http.Request) bool }); ok {
		present = present || a.RequestIdentityPresent(r)
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		if optional && !present && errors.Is(e, authority.ErrInvalid) {
			return r.Context(), "", nil
		}
		return r.Context(), "", e
	}
	if permission == "" {
		p, e = s.Authority.AuthenticatePrincipal(r.Context(), p)
	} else {
		p, e = s.Authority.AuthorizePrincipal(r.Context(), p, permission)
	}
	if e != nil {
		return r.Context(), "", e
	}
	if !ValidID(p.Account.ID) {
		return r.Context(), "", authority.ErrInvalid
	}
	ctx := r.Context()
	if s.Bind != nil {
		ctx = s.Bind(ctx, p)
	}
	return ctx, p.Account.ID, nil
}
func (s *Service) serveDetails(w http.ResponseWriter, r *http.Request, p []string) bool {
	slug := len(p) == 3 && p[0] == "public"
	online := !slug && len(p) == 3 && p[1] == "members" && p[2] == "online"
	profile := len(p) == 4 && p[1] == "members" && p[2] == "me" && p[3] == "profile"
	deletion := len(p) == 4 && p[0] == "rooms" && p[2] == "messages"
	timeout := len(p) == 5 && p[0] == "rooms" && p[2] == "members" && p[4] == "timeout"
	if !slug && !online && !profile && !deletion && !timeout {
		return false
	}
	allow := "GET"
	permission := ""
	if profile {
		allow = "PATCH, DELETE"
		if r.Method == "PATCH" {
			permission = "chat.members.manage"
		}
	}
	if deletion {
		allow = "DELETE"
		permission = "chat.messages.delete"
	}
	if timeout {
		allow = "POST"
		permission = "chat.members.timeout"
	}
	if (slug || online) && r.Method != "GET" || profile && r.Method != "PATCH" && r.Method != "DELETE" || deletion && r.Method != "DELETE" || timeout && r.Method != "POST" {
		method(w, allow)
		return true
	}
	if r.URL.RawQuery != "" {
		failure(w, ErrInvalid)
		return true
	}
	if (online || profile) && !ValidID(p[0]) || deletion && (!ValidID(p[1]) || !ValidID(p[3])) || timeout && (!ValidID(p[1]) || !ValidID(p[3])) {
		failure(w, ErrInvalid)
		return true
	}
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return true
	}
	repo, ok := s.Repository.(DetailRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return true
	}
	ctx, actor, e := s.detailIdentity(r, permission, slug || online)
	if e != nil {
		failure(w, e)
		return true
	}
	var result any
	status := 200
	switch {
	case slug:
		result, e = repo.PublicBySlug(ctx, actor, p[1], p[2])
	case online:
		result, e = repo.OnlineMembers(ctx, actor, p[0])
	case profile:
		if r.Method == "PATCH" {
			var q ProfileRequest
			e = decode(w, r, &q, "nick")
			if e == nil {
				e = ValidateProfile(q)
			}
			if e == nil {
				result, e = repo.UpdateProfile(ctx, actor, p[0], q)
			}
		} else {
			e = empty(w, r)
			if e == nil {
				result, e = repo.ClearProfile(ctx, actor, p[0])
			}
		}
	case deletion:
		var q ModerationDelete
		e = decode(w, r, &q, "reason")
		if e == nil {
			e = ValidateModerationReason(q.Reason)
		}
		if e == nil {
			e = repo.ModerateDelete(ctx, actor, p[1], p[3], q)
		}
		status = 204
	case timeout:
		var q ModerationTimeout
		e = decode(w, r, &q, "reason", "duration_minutes")
		if e == nil && (q.DurationMinutes < 1 || q.DurationMinutes > 30*24*60) {
			e = ErrInvalid
		}
		if e == nil {
			e = ValidateModerationReason(q.Reason)
		}
		if e == nil {
			result, e = repo.ModerateTimeout(ctx, actor, p[1], p[3], q)
		}
	}
	if e != nil {
		failure(w, e)
	} else {
		send(w, status, result)
	}
	return true
}
