// SPDX-License-Identifier: AGPL-3.0-only
package locations

import "net/http"

func nearbyHTTP(w http.ResponseWriter, r *http.Request, base Repository, auth Auth, actor string, parts []string) {
	if actor == "" {
		failure(w, ErrUnauthenticated)
		return
	}
	repo, ok := base.(NearbyRepository)
	if !ok {
		failure(w, ErrUnavailable)
		return
	}
	if len(parts) != 1 || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var permission string
	switch parts[0] {
	case "presence-tokens":
		permission = "nearby.presence.manage"
	case "resolve":
		permission = "nearby.resolve"
	default:
		http.NotFound(w, r)
		return
	}
	if e := auth.Authorize(r, permission); e != nil {
		failure(w, e)
		return
	}
	if _, e := query(r); e != nil {
		failure(w, e)
		return
	}
	var out any
	var e error
	if parts[0] == "presence-tokens" {
		var v PresenceRequest
		e = decode(w, r, &v)
		if e == nil {
			out, e = repo.IssuePresenceTokens(r.Context(), actor, v)
		}
	} else {
		var v struct {
			Observations []Observation `json:"observations"`
		}
		e = decode(w, r, &v)
		if e == nil {
			var peers []NearbyPeer
			peers, e = repo.ResolveNearby(r.Context(), actor, v.Observations)
			out = map[string]any{"peers": peers}
		}
	}
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, out)
}
