// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/passkitpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/passkit"
)

type passkitModules struct {
	repo              *passkitpg.Repository
	member, callbacks http.Handler
}

func passkitFor(db *sql.DB, c Config, v *authority.Verifier, g *authoritypg.TransactionGuard) (out passkitModules, e error) {
	out.callbacks = (&passkit.Service{}).Handler()
	out.member = out.callbacks
	if c.AppleWallet == nil {
		return out, nil
	}
	if g == nil {
		return out, passkit.ErrUnavailable
	}
	config := *c.AppleWallet
	if config.Now == nil {
		config.Now = c.Authority.Now
	}
	generator, e := passkit.NewGenerator(config)
	if e != nil {
		return out, e
	}
	out.repo, e = passkitpg.New(db, g, passkitpg.Config{Generator: generator, Media: c.MediaStore, ProgramGroup: c.AppleWalletProgramGroup})
	if e != nil {
		return out, e
	}
	service := (&passkit.Service{Repository: out.repo, Authority: proofIdentity{identity{v}}}).Handler()
	out.member = bindIdentity(v, bindTransactionAuthority(g, service))
	out.callbacks = service
	return out, nil
}
func (m passkitModules) route(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	for _, prefix := range []string{"/passport/", "/stargate/"} {
		if strings.HasPrefix(p, prefix) {
			p = "/api/" + strings.TrimPrefix(p, prefix)
		}
	}
	var h http.Handler
	if p == "/api/accounts/me/passbook/member" {
		h = m.member
	} else if under(p, "/api/passkit/v1") {
		h = m.callbacks
	}
	if h == nil {
		return false
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Path = p
	copy.URL = &u
	h.ServeHTTP(w, copy)
	return true
}
func (m passkitModules) ready(ctx context.Context) error {
	if m.repo == nil {
		return nil
	}
	return m.repo.Ready(ctx)
}
