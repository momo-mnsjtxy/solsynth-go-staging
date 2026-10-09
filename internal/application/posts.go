// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"solsynth.local/go-migration/ports/authority"
)

type postIdentity struct{ identity }

func (a postIdentity) Authenticate(r *http.Request) (string, error) {
	if _, ok := r.Context().Value(principalKey{}).(authority.Principal); !ok {
		return "", nil // Domain permits this only for explicit public read routes.
	}
	p, err := a.require(r.Context())
	if err != nil {
		return "", err
	}
	return p.Account.ID, nil
}
func (a postIdentity) Authorize(r *http.Request, permission string) error {
	_, err := a.require(r.Context(), permission)
	return err
}
