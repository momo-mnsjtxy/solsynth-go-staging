// SPDX-License-Identifier: AGPL-3.0-only
package locations

import "net/http"

func geocodingHTTP(w http.ResponseWriter, r *http.Request, base Repository, auth Auth, actor string, parts []string) {
	if len(parts) != 1 || parts[0] != "geocode" || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	if actor == "" {
		failure(w, ErrUnauthenticated)
		return
	}
	if e := auth.Authorize(r, "locations.geocode"); e != nil {
		failure(w, e)
		return
	}
	if _, e := query(r); e != nil {
		failure(w, e)
		return
	}
	var v GeocodeRequest
	if e := decode(w, r, &v); e != nil {
		failure(w, e)
		return
	}
	repo, ok := base.(GeocodingRepository)
	if !ok {
		failure(w, ErrUnavailable)
		return
	}
	out, e := repo.Geocode(r.Context(), actor, v)
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, map[string]any{"places": out})
}
