// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

func organizationRoute(p []string) bool {
	return len(p) == 1 && (p[0] == "summary" || p[0] == "read-all" || p[0] == "groups") || len(p) == 2 && (p[0] == "groups" || p[0] == "messages" && p[1] == "search" || p[1] == "pins") || len(p) == 3 && (p[1] == "pins" || p[0] == "rooms" && p[2] == "group")
}
func (s *Service) serveOrganization(w http.ResponseWriter, r *http.Request, p []string) bool {
	if !organizationRoute(p) {
		return false
	}
	if s == nil || s.Organization == nil {
		failure(w, ErrUnavailable)
		return true
	}
	repo := s.Organization
	allow, permission := "GET", ""
	switch {
	case len(p) == 1 && p[0] == "read-all":
		allow = "POST"
		permission = "chat.read.all"
	case len(p) == 1 && p[0] == "groups":
		allow = "GET, POST"
		if r.Method == "POST" {
			permission = "chat.groups.manage"
		}
	case len(p) == 2 && p[0] == "groups":
		allow = "PATCH, DELETE"
	case len(p) == 3 && p[0] == "rooms":
		allow = "PATCH"
	case len(p) == 2 && p[1] == "pins":
		allow = "GET, POST"
		if r.Method == "POST" {
			permission = "chat.pins.manage"
		}
	case len(p) == 3 && p[1] == "pins":
		allow = "DELETE"
		permission = "chat.pins.manage"
	}
	if !strings.Contains(", "+allow+",", ", "+r.Method+",") {
		method(w, allow)
		return true
	}
	anonymous := r.Method == "GET" && len(p) == 2 && p[1] == "pins"
	actor := ""
	ctx := r.Context()
	var principal authority.Principal
	var e error
	if s.Authority == nil {
		failure(w, authority.ErrUnavailable)
		return true
	}
	principal, e = s.Authority.AuthenticateRequest(r)
	if e != nil {
		if !(anonymous && errors.Is(e, authority.ErrInvalid) && len(r.Header.Values("Authorization")) == 0) {
			failure(w, e)
			return true
		}
		e = nil
	} else {
		if permission == "" {
			principal, e = s.Authority.AuthenticatePrincipal(ctx, principal)
		} else {
			principal, e = s.Authority.AuthorizePrincipal(ctx, principal, permission)
		}
		if e != nil {
			failure(w, e)
			return true
		}
		actor = principal.Account.ID
		if !ValidID(actor) {
			failure(w, authority.ErrInvalid)
			return true
		}
		if s.Bind != nil {
			ctx = s.Bind(ctx, principal)
		}
	}

	query, parseError := url.ParseQuery(r.URL.RawQuery)
	if parseError != nil {
		failure(w, ErrInvalid)
		return true
	}
	for key, v := range query {
		if len(v) != 1 && !(len(p) == 2 && p[0] == "messages" && p[1] == "search" && key == "room_ids") {
			failure(w, ErrInvalid)
			return true
		}
	}
	var out any
	switch {
	case len(p) == 1 && p[0] == "summary":
		if r.URL.RawQuery != "" {
			e = ErrInvalid
			break
		}
		out, e = repo.Summary(ctx, actor)
	case len(p) == 1 && p[0] == "read-all":
		if r.URL.RawQuery != "" {
			e = ErrInvalid
			break
		}
		if e = empty(w, r); e == nil {
			e = repo.ReadAll(ctx, actor)
		}
	case len(p) == 2 && p[0] == "messages" && p[1] == "search":
		q := SearchRequest{Query: query.Get("query"), Sender: query.Get("sender"), RoomIDs: query["room_ids"], Take: 20}
		for key, v := range query {
			switch key {
			case "query", "sender", "room_ids":
			case "take", "offset":
				var n int
				n, e = strconv.Atoi(v[0])
				if key == "take" {
					q.Take = n
				} else {
					q.Offset = n
				}
			case "after", "before":
				var at time.Time
				at, e = time.Parse(time.RFC3339Nano, v[0])
				if key == "after" {
					q.After = &at
				} else {
					q.Before = &at
				}
			default:
				e = ErrInvalid
			}
			if e != nil {
				e = ErrInvalid
				break
			}
		}
		if e != nil {
			break
		}
		q, e = ValidateSearch(q)
		if e != nil {
			break
		}
		var result SearchPage
		result, e = repo.Search(ctx, actor, q)
		if e == nil {
			w.Header().Set("X-Total", strconv.FormatInt(result.Total, 10))
			out = result.Rooms
		}
	case len(p) == 1 && p[0] == "groups":
		if r.URL.RawQuery != "" {
			e = ErrInvalid
			break
		}
		if r.Method == "GET" {
			out, e = repo.Groups(ctx, actor)
		} else {
			var q GroupPatch
			if e = decode(w, r, &q, "name", "color", "icon", "order"); e == nil {
				e = ValidateGroup(q, true)
			}
			if e == nil {
				out, e = repo.CreateGroup(ctx, actor, q)
			}
		}
	case len(p) == 2 && p[0] == "groups":
		if !ValidID(p[1]) || r.URL.RawQuery != "" {
			e = ErrInvalid
			break
		}
		if r.Method == "DELETE" {
			if e = empty(w, r); e == nil {
				e = repo.DeleteGroup(ctx, actor, p[1])
			}
		} else {
			var q GroupPatch
			if e = decode(w, r, &q, "name", "color", "icon", "order"); e == nil {
				e = ValidateGroup(q, false)
			}
			if e == nil {
				out, e = repo.UpdateGroup(ctx, actor, p[1], q)
			}
		}
	case len(p) == 3 && p[0] == "rooms":
		if !ValidID(p[1]) || r.URL.RawQuery != "" {
			e = ErrInvalid
			break
		}
		var q struct {
			GroupID *string `json:"group_id"`
		}
		if e = decode(w, r, &q, "group_id"); e == nil {
			if q.GroupID != nil && !ValidID(*q.GroupID) {
				e = ErrInvalid
			} else {
				e = repo.MoveToGroup(ctx, actor, p[1], q.GroupID)
			}
		}
	case len(p) == 2 && p[1] == "pins":
		if !ValidID(p[0]) {
			e = ErrInvalid
			break
		}
		if r.Method == "GET" {
			expired := false
			for key, v := range query {
				if key != "includeExpired" {
					e = ErrInvalid
					break
				}
				if !strings.EqualFold(v[0], "true") && !strings.EqualFold(v[0], "false") {
					e = ErrInvalid
					break
				}
				expired, e = strconv.ParseBool(strings.ToLower(v[0]))
				if e != nil {
					e = ErrInvalid
					break
				}
			}
			if e == nil {
				out, e = repo.Pins(ctx, actor, p[0], expired)
			}
		} else {
			if r.URL.RawQuery != "" {
				e = ErrInvalid
				break
			}
			var q PinRequest
			if e = decode(w, r, &q, "message_id", "expires_at"); e == nil {
				out, e = repo.Pin(ctx, actor, p[0], q)
			}
		}
	case len(p) == 3 && p[1] == "pins":
		if !ValidID(p[0]) || !ValidID(p[2]) || r.URL.RawQuery != "" {
			e = ErrInvalid
			break
		}
		if e = empty(w, r); e == nil {
			e = repo.Unpin(ctx, actor, p[0], p[2])
		}
	}
	if e != nil {
		failure(w, e)
	} else {
		send(w, 200, out)
	}
	return true
}
