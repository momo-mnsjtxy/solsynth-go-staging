// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"net/http"
	"solsynth.local/go-migration/ports/authority"
)

func encryptedNFCHTTP(w http.ResponseWriter, r *http.Request, base Repository, auth Auth, actor string, parts []string, admin bool) {
	if actor == "" {
		failure(w, ErrUnauthenticated)
		return
	}
	repo, ok := base.(EncryptedNFCRepository)
	if !ok {
		failure(w, ErrUnavailable)
		return
	}
	if _, e := query(r); e != nil {
		failure(w, e)
		return
	}
	permission := ""
	claim := false
	if admin && len(parts) == 1 && parts[0] == "tags" && (r.Method == "GET" || r.Method == "POST") {
		permission = "nfc.admin.manage"
	} else if !admin && r.Method == "POST" {
		if len(parts) == 1 && parts[0] == "sun" {
			permission = "nfc.tags.read"
		} else if len(parts) == 2 && parts[1] == "claim" {
			permission = "nfc.tags.claim"
			claim = true
		} else if len(parts) == 3 && parts[2] == "lock" && authority.ValidID(parts[1]) {
			permission = "nfc.tags.lock"
		}
	}
	if permission == "" {
		http.NotFound(w, r)
		return
	}
	if e := auth.Authorize(r, permission); e != nil {
		failure(w, e)
		return
	}
	var out any
	var e error
	if admin {
		if r.Method == "GET" {
			out, e = repo.EncryptedTags(r.Context(), actor)
		} else {
			var v ProvisionTag
			e = decode(w, r, &v)
			if e == nil {
				out, e = repo.ProvisionEncryptedTag(r.Context(), actor, v)
			}
		}
	} else if permission == "nfc.tags.lock" {
		e = empty(w, r)
		if e == nil {
			out, e = repo.LockEncryptedTag(r.Context(), actor, parts[1])
		}
	} else {
		var v SUNRequest
		e = decode(w, r, &v)
		if e == nil {
			out, e = repo.ResolveSUN(r.Context(), actor, v, claim)
		}
	}
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, out)
}
