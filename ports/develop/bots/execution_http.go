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
	if (len(parts) != 6 && len(parts) != 7) || !authority.ValidID(parts[5]) || len(parts) == 7 && parts[6] != "renew" && parts[6] != "renewal" {
		failure(w, ErrInvalid, false)
		return
	}
	if len(parts) == 7 && parts[6] == "renewal" {
		renewals, supported := s.Repository.(RenewalRepository)
		if !supported {
			failure(w, ErrUnsupported, false)
			return
		}
		action := "read"
		var request *RenewalPolicyRequest
		switch r.Method {
		case "GET":
		case "PUT":
			action, request = "set", &RenewalPolicyRequest{}
			if e := decode(w, r, request); e != nil {
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
		value, e := renewals.RenewalPolicy(ctx, scope, bot, parts[5], request, action)
		if e != nil {
			failure(w, e, false)
			return
		}
		if action == "delete" {
			send(w, 204, nil)
			return
		}
		send(w, 200, value)
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
	case "POST":
		if len(parts) != 7 {
			w.Header().Set("Allow", "GET, PUT, DELETE")
			w.WriteHeader(405)
			return
		}
		action = "renew"
		var renewal RenewalRequest
		if e := decode(w, r, &renewal); e != nil {
			failure(w, e, false)
			return
		}
		p = &EnrollmentRequest{ExpiresAt: renewal.ExpiresAt}
	case "DELETE":
		action = "delete"
		if e := empty(w, r); e != nil {
			failure(w, e, false)
			return
		}
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE, POST")
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
