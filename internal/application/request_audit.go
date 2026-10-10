// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"solsynth.local/go-migration/internal/auditcapture"
	"solsynth.local/go-migration/ports/authority"
)

func captureRequestAudit(v *authority.Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v != nil && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if p, e := v.AuthenticateRequest(r); e == nil {
				r = r.WithContext(auditcapture.Bind(r.Context(), r, p))
			}
		}
		next.ServeHTTP(w, r)
	})
}
