// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/ipcheck"
)

type ipcheckIdentity struct{ identity }

func (i ipcheckIdentity) AuthorizeDiagnostic(r *http.Request) error {
	p, e := i.require(r.Context(), "admin.ip.check")
	if e != nil {
		return e
	}
	if !p.Account.Superuser {
		return authority.ErrForbidden
	}
	return nil
}
func ipCheckHandler(c Config, v *authority.Verifier) (http.Handler, error) {
	cfg := ipcheck.Config{}
	if c.IPCheck != nil {
		cfg = *c.IPCheck
	}
	s, e := ipcheck.New(cfg, ipcheckIdentity{identity{v}})
	if e != nil {
		return nil, e
	}
	return bindIdentity(v, s), nil
}
