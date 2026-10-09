// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"context"
	"net/http"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
)

func RoomCommandsRoute(p string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	return len(parts) == 5 && parts[0] == "api" && parts[1] == "chat" && parts[3] == "bots" && parts[4] == "commands"
}
func (s *Service) executionHTTP(w http.ResponseWriter, r *http.Request, ctx context.Context, scope apps.Scope, bot string, parts []string) {
	repo, ok := s.Repository.(ExecutionRepository)
	if !ok {
		failure(w, ErrUnsupported, false)
		return
	}
	if len(parts) != 6 || !authority.ValidID(parts[5]) {
		failure(w, ErrInvalid, false)
		return
	}
	var p *EnrollmentRequest
	action := "read"
	switch r.Method {
	case "GET":
	case "PUT":
		action = "set"
		p = &EnrollmentRequest{}
		if e := decode(w, r, p); e != nil {
			failure(w, e, false)
			return
		}
	case "DELETE":
		action = "delete"
		if e := empty(w, r); e != nil {
			failure(w, e, false)
			return
		}
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		w.WriteHeader(405)
		return
	}
	v, e := repo.Enrollment(ctx, scope, bot, parts[5], p, action)
	if e != nil {
		failure(w, e, false)
		return
	}
	if action == "delete" {
		send(w, 204, nil)
		return
	}
	send(w, 200, v)
}
