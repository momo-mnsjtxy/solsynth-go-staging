// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"errors"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"strconv"
)

type ThreadReply struct {
	Message messager.MessageProjection `json:"message"`
	Depth   int                        `json:"depth"`
}
type ThreadPage struct {
	Root    messager.MessageProjection `json:"root"`
	Replies []ThreadReply              `json:"replies"`
	Total   int64                      `json:"-"`
}
type ThreadRepository interface {
	Thread(context.Context, string, string, string, int, int) (ThreadPage, error)
	Redirect(context.Context, string, string, []string, string) (messager.MessageProjection, error)
}
type RedirectRequest struct {
	MessageIDs []string `json:"message_ids"`
}

func (s *Service) serveThread(w http.ResponseWriter, r *http.Request, p []string) {
	repo, ok := s.Repository.(ThreadRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	if r.Method != "GET" {
		method(w, "GET")
		return
	}
	if !ValidID(p[0]) || !ValidID(p[2]) {
		failure(w, ErrInvalid)
		return
	}
	take, offset := 20, 0
	for key, values := range r.URL.Query() {
		if len(values) != 1 || (key != "take" && key != "offset") {
			failure(w, ErrInvalid)
			return
		}
		n, e := strconv.Atoi(values[0])
		if e != nil {
			failure(w, ErrInvalid)
			return
		}
		if key == "take" {
			take = n
		} else {
			offset = n
		}
	}
	if take < 1 || take > 100 || offset < 0 || offset > 100000 {
		failure(w, ErrInvalid)
		return
	}
	actor, e := (readIdentity{s}).Account(r)
	if e != nil {
		if errors.Is(e, messager.ErrSyncUnauthorized) {
			failure(w, authority.ErrInvalid)
		} else {
			failure(w, ErrForbidden)
		}
		return
	}
	ctx := r.Context()
	if actor != "" {
		principal, e := s.principal(r, "")
		if e != nil {
			failure(w, e)
			return
		}
		if s.Bind != nil {
			ctx = s.Bind(ctx, principal)
		}
	}
	out, e := repo.Thread(ctx, actor, p[0], p[2], take, offset)
	if e != nil {
		failure(w, e)
		return
	}
	w.Header().Set("X-Total", strconv.FormatInt(out.Total, 10))
	send(w, 200, out)
}
