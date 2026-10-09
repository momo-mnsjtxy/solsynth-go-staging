// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
)

func IsQuoteRoute(p string) bool {
	return p == "/quote-authorizations" || strings.HasPrefix(p, "/quote-authorizations/")
}
func (h Handler) quote(w http.ResponseWriter, r *http.Request) {
	repository, ok := h.Repository.(f.QuoteRepository)
	if !ok {
		fail(w, f.ErrUnavailable)
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, f.ErrInvalid)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/quote-authorizations/")
	if r.Method == "GET" {
		if !authority.ValidID(id) {
			fail(w, f.ErrNotFound)
			return
		}
		result, e := repository.QuoteAuthorization(r.Context(), id)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, result, f.MediaType)
		return
	}
	if r.Method != "DELETE" && r.Method != "POST" {
		method(w, "GET, DELETE")
		return
	}
	if h.Authority == nil {
		fail(w, authority.ErrUnavailable)
		return
	}
	actor, e := h.Authority.AuthenticateRequest(r)
	if e != nil {
		fail(w, e)
		return
	}
	actor, e = h.Authority.AuthorizePrincipal(r.Context(), actor, f.Permission)
	if e != nil {
		fail(w, e)
		return
	}
	// The upstream bearer subject to remote actor mapping does not establish
	// quote ownership. Only the signed inbox transition can create a stamp.
	if r.Method == "POST" {
		fail(w, f.ErrUnsupported)
		return
	}
	if !authority.ValidID(id) {
		fail(w, f.ErrNotFound)
		return
	}
	if e = repository.RevokeQuoteAuthorization(r.Context(), actor.Account.ID, id); e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]bool{"revoked": true}, "application/json")
}
