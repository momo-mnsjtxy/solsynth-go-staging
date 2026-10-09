// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"net/http"
	"strconv"
)

func (s *Service) permissionHTTP(w http.ResponseWriter, r *http.Request, p []string) bool {
	if len(p) < 2 || p[1] != "permissions" {
		return false
	}
	repo, ok := s.Repository.(PermissionRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return true
	}
	if r.URL.RawQuery != "" {
		failure(w, ErrInvalid)
		return true
	}
	keys := []string{}
	if r.Method != "GET" {
		keys = []string{"realms.permissions.manage"}
	}
	actor, e := s.principal(r, keys...)
	if e != nil {
		failure(w, e)
		return true
	}
	ctx := r.Context()
	var out any
	status := 200
	switch {
	case len(p) == 3 && p[2] == "roles":
		switch r.Method {
		case "GET":
			out, e = repo.RolePermissions(ctx, actor, p[0])
		case "POST":
			var body RolePermissionRequest
			e = decode(w, r, &body, false)
			if e == nil {
				out, e = repo.SetRolePermission(ctx, actor, p[0], body)
			}
		default:
			status = 405
		}
	case len(p) == 4 && p[2] == "roles" && r.Method == "DELETE":
		n, x := strconv.Atoi(p[3])
		if x != nil || n < 0 || n >= 100 {
			e = ErrInvalid
		} else {
			var body struct{}
			e = decode(w, r, &body, true)
			if e == nil {
				e = repo.DeleteRolePermission(ctx, actor, p[0], Role(n))
				status = 204
			}
		}
	case len(p) == 4 && p[2] == "users" && r.Method == "GET":
		out, e = repo.UserPermissions(ctx, actor, p[0], p[3])
	case len(p) == 3 && p[2] == "users" && r.Method == "POST":
		var body UserPermissionRequest
		e = decode(w, r, &body, false)
		if e == nil {
			out, e = repo.SetUserPermission(ctx, actor, p[0], body)
		}
	case len(p) == 3 && p[2] == "me" && r.Method == "GET":
		out, e = repo.EffectivePermissions(ctx, actor, p[0])
	default:
		status = 405
	}
	if e != nil {
		failure(w, e)
	} else if status == 405 {
		w.Header().Set("Allow", "GET, POST, DELETE")
		send(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
	} else {
		send(w, status, out)
	}
	return true
}
