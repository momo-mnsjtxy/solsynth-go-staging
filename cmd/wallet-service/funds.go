// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
)

// Bind verified authority for atomic payment and fund writes. The handler owns
// public-read routing, so an absent bearer reaches its normal fail-closed gate.
func bindWalletAuthority(v *authority.Verifier, g *authoritypg.TransactionGuard, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			p, e := v.AuthenticateRequest(r)
			if e != nil {
				http.Error(w, "Authentication failed.", http.StatusUnauthorized)
				return
			}
			r = r.WithContext(g.Bind(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}
