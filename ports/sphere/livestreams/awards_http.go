// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"net/http"
	"strconv"
)

func (s *Service) awardsHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, actor string, parts []string, limit, offset int) {
	repo, ok := s.Repository.(AwardRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	var out any
	var e error
	total := -1
	switch {
	case len(parts) == 2:
		switch r.Method {
		case "GET":
			out, total, e = repo.ListAwards(ctx, actor, parts[0], limit, offset, false)
		case "POST":
			var q AwardRequest
			e = decode(w, r, &q, "amount", "message")
			if len(r.Header.Values("Idempotency-Key")) > 1 {
				e = ErrInvalid
			}
			if e == nil {
				out, e = repo.CreateAwardOrder(ctx, actor, parts[0], r.Header.Get("Idempotency-Key"), q)
			}
		default:
			method(w, "GET, POST")
			return
		}
	case len(parts) == 3 && parts[2] == "active":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		out, total, e = repo.ListAwards(ctx, actor, parts[0], 100, 0, true)
	case len(parts) == 3 && parts[2] == "leaderboard":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		out, e = repo.AwardLeaderboard(ctx, actor, parts[0], limit)
	default:
		failure(w, ErrNotFound)
		return
	}
	if e != nil {
		failure(w, e)
		return
	}
	if total >= 0 {
		w.Header().Set("X-Total", strconv.Itoa(total))
	}
	respond(w, 200, out)
}
