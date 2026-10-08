// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"reflect"
)

// AuthenticatePrincipal is an authentication-only freshness check for routes
// whose evidenced contract requires account/session identity without a named
// permission. It accepts only this verifier's original proof-bearing principal
// and rechecks current account/session/version/epoch state. It does not grant
// permissions or relax AuthorizePrincipal's nonempty-permission requirement.
func (s *Service) AuthenticatePrincipal(ctx context.Context, p Principal) (Principal, error) {
	if p.proof == nil || p.proof.service != s || !reflect.DeepEqual(p.Account, p.proof.account) || !reflect.DeepEqual(p.Session, p.proof.session) {
		return Principal{}, ErrInvalid
	}
	return s.withClaims(ctx, p.proof.claims, nil)
}
func (v *Verifier) AuthenticatePrincipal(ctx context.Context, p Principal) (Principal, error) {
	return v.service.AuthenticatePrincipal(ctx, p)
}
