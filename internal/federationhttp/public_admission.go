// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	f "solsynth.local/go-migration/ports/federation"
)

func (h Handler) publicAdmission(w http.ResponseWriter, r *http.Request, actor, publisher string) {
	repo, ok := h.Repository.(f.PublicAdmissionRepository)
	if !ok {
		fail(w, f.ErrUnavailable)
		return
	}
	var target f.DiscoveryTarget
	var job f.DiscoveryJob
	var e error
	switch r.Method {
	case "POST":
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.URL.RawQuery != "" {
			fail(w, f.ErrInvalid)
			return
		}
		body, err := readBody(w, r, 16384)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(body, &fields) != nil || len(fields) != 2 || fields["resource"] == nil || fields["actor"] == nil || json.Unmarshal(body, &target) != nil || target.Resource == "" || target.Actor == "" {
			fail(w, f.ErrInvalid)
			return
		}
		job, e = repo.RequestPublicAdmission(r.Context(), actor, publisher, target)
	case "GET":
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 2 || len(q["resource"]) != 1 || len(q["actor"]) != 1 {
			fail(w, f.ErrInvalid)
			return
		}
		target = f.DiscoveryTarget{Resource: q.Get("resource"), Actor: q.Get("actor")}
		job, e = repo.PublicAdmission(r.Context(), actor, publisher, target)
	default:
		method(w, "GET, POST")
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, job, "application/json")
}
