// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"net/http"
	"strconv"
)

func (s *Service) operationsHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, actor string, parts []string, limit, offset int) {
	repo, ok := s.Repository.(OperationRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	var out any
	var e error
	switch {
	case len(parts) == 1 && r.Method == "GET":
		kind := r.URL.Query().Get("kind")
		var total int
		out, total, e = repo.InspectOperations(ctx, actor, kind, limit, offset)
		if e == nil {
			w.Header().Set("X-Total", strconv.Itoa(total))
		}
	case len(parts) == 4 && parts[3] == "redrive" && r.Method == "POST":
		e = empty(w, r)
		if e == nil {
			out, e = repo.RedriveOperation(ctx, actor, parts[1], parts[2])
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
