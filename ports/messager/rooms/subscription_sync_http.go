// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import "net/http"

func (s *Service) serveSubscriptionSync(w http.ResponseWriter, r *http.Request, p []string) bool {
	global := len(p) == 1 && p[0] == "sync"
	joined := len(p) == 2 && p[0] == "rooms" && p[1] == "sync"
	room := len(p) == 2 && p[1] == "subscriptions"
	mine := len(p) == 3 && p[0] == "accounts" && p[1] == "me" && (p[2] == "subscriptions" || p[2] == "status")
	if !global && !joined && !room && !mine {
		return false
	}
	allow, permission := "GET", ""
	if global || joined {
		allow, permission = "POST", "chat.sync"
	}
	if r.Method != allow {
		method(w, allow)
		return true
	}
	if r.URL.RawQuery != "" || room && !ValidID(p[0]) {
		failure(w, ErrInvalid)
		return true
	}
	ctx, actor, e := s.detailIdentity(r, permission, false)
	if e != nil {
		failure(w, e)
		return true
	}
	repo, ok := s.Organization.(SubscriptionSyncRepository)
	if !ok {
		failure(w, ErrUnavailable)
		return true
	}
	var out any
	switch {
	case global:
		var q GlobalSyncRequest
		e = decode(w, r, &q, "last_sync_timestamp", "last_sync_message_id", "missing_sequences", "missing_sequence_ranges")
		if e == nil {
			e = ValidateGlobalSync(q)
		}
		if e == nil {
			out, e = repo.GlobalSync(ctx, actor, q)
		}
	case joined:
		var q JoinedRoomSyncRequest
		e = decode(w, r, &q, "last_sync_timestamp")
		if e == nil && !ValidSyncTimestamp(q.LastSyncTimestamp) {
			e = ErrInvalid
		}
		if e == nil {
			out, e = repo.JoinedRoomSync(ctx, actor, q)
		}
	default:
		e = empty(w, r)
		if e == nil {
			switch {
			case room:
				out, e = repo.RoomSubscriptions(ctx, actor, p[0])
			case p[2] == "status":
				out, e = repo.ChatStatus(ctx, actor)
			default:
				out, e = repo.AccountSubscriptions(ctx, actor)
			}
		}
	}
	if e != nil {
		failure(w, e)
	} else {
		send(w, 200, out)
	}
	return true
}
