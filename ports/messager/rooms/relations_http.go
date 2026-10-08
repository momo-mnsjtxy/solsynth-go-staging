// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (s *Service) serveRealm(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) != 4 || p[0] != "api" || p[1] != "realms" || p[2] == "" || p[3] != "chat" {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" {
		method(w, "GET")
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	take, offset := 20, 0
	for k, values := range q {
		if len(values) != 1 || (k != "take" && k != "offset") {
			failure(w, ErrInvalid)
			return
		}
		n, x := strconv.Atoi(values[0])
		if x != nil {
			failure(w, ErrInvalid)
			return
		}
		if k == "take" {
			take = n
		} else {
			offset = n
		}
	}
	if take < 1 || take > 100 || offset < 0 || offset > 100000 {
		failure(w, ErrInvalid)
		return
	}
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	relations, ok := s.Repository.(RelationRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	pov, e := s.principal(r, "")
	if e != nil {
		failure(w, e)
		return
	}
	ctx := r.Context()
	if s.Bind != nil {
		ctx = s.Bind(ctx, pov)
	}
	rows, e := relations.RealmRooms(ctx, pov.Account.ID, p[2], take, offset)
	if e != nil {
		failure(w, e)
		return
	}
	send(w, 200, rows)
}
