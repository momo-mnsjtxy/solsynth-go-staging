// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
)

func (s *Service) chatHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, actor string, parts []string, limit, offset int) {
	repo, ok := s.Repository.(ChatRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	if len(r.Header.Values("Idempotency-Key")) > 1 {
		failure(w, ErrInvalid)
		return
	}
	var out any
	var e error
	switch {
	case len(parts) == 2:
		switch r.Method {
		case "GET":
			out, e = repo.ListChat(ctx, actor, parts[0], limit, offset)
		case "POST":
			var q struct {
				Content string `json:"content"`
			}
			e = decode(w, r, &q, "content")
			if e == nil {
				out, e = repo.SendChat(ctx, actor, parts[0], q.Content, r.Header.Get("Idempotency-Key"))
			}
		default:
			method(w, "GET, POST")
			return
		}
	case len(parts) == 3 && authority.ValidID(parts[2]):
		if r.Method != "DELETE" {
			method(w, "DELETE")
			return
		}
		e = empty(w, r)
		if e == nil {
			e = repo.DeleteChat(ctx, actor, parts[0], parts[2])
		}
	case len(parts) == 4 && authority.ValidID(parts[2]) && parts[3] == "timeout":
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		var q ChatTimeout
		e = decode(w, r, &q, "duration_minutes")
		if e == nil {
			e = repo.TimeoutChat(ctx, actor, parts[0], parts[2], r.Header.Get("Idempotency-Key"), q)
		}
	default:
		failure(w, ErrNotFound)
		return
	}
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, out)
}
