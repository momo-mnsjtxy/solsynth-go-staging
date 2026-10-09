// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
)

// bindTransactionAuthority runs after bindIdentity removed bearer bytes. The
// domain guard receives the same original proof, never an account-ID assertion.
func bindTransactionAuthority(guard *authoritypg.TransactionGuard, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if guard != nil {
			if p, ok := r.Context().Value(principalKey{}).(authority.Principal); ok {
				r = r.WithContext(guard.Bind(r.Context(), p))
			}
		}
		next.ServeHTTP(w, r)
	})
}
