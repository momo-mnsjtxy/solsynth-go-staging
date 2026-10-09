// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/internal/adminpg"
	"solsynth.local/go-migration/internal/authoritypg"
	adminaccounts "solsynth.local/go-migration/ports/admin/accounts"
	"solsynth.local/go-migration/ports/admin/statistics"
	"solsynth.local/go-migration/ports/admin/verification"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/automod"
	"time"
)

type adminModules struct {
	repo                                                       *adminpg.Repository
	verification, automod, qualified, passport, sphere, wallet http.Handler
	accounts                                                   http.Handler
}

func adminModulesFor(db *sql.DB, g *authoritypg.TransactionGuard, v *authority.Verifier, now func() time.Time) (out adminModules, err error) {
	if g != nil {
		out.repo, err = adminpg.New(db, g, now)
		if err != nil {
			return
		}
	}
	wrap := func(h http.Handler) http.Handler { return bindIdentity(v, bindTransactionAuthority(g, h)) }
	auth := postIdentity{identity{v}}
	// Avoid typed nil repositories inside interfaces on the unavailable path.
	var vr verification.Repository
	var ar automod.Repository
	var sr statistics.Repository
	var acr adminaccounts.Repository
	if out.repo != nil {
		vr = out.repo
		ar = out.repo
		sr = out.repo
		acr = out.repo
	}
	out.verification = wrap(verification.NewHandler(vr, auth))
	out.automod = wrap(automod.NewHandler(ar, auth))
	out.qualified = wrap(statistics.NewHandler(sr, auth, ""))
	out.passport = wrap(statistics.NewHandler(sr, auth, statistics.Passport))
	out.sphere = wrap(statistics.NewHandler(sr, auth, statistics.Sphere))
	out.wallet = wrap(statistics.NewHandler(sr, auth, statistics.Wallet))
	out.accounts = wrap(adminaccounts.NewHandler(acr, auth))
	return
}
func (a adminModules) route(service, path string) http.Handler {
	if adminaccounts.Handles(path) && (service == "api" || service == "passport") {
		return a.accounts
	}
	if path == "/api/admin/stats" {
		switch service {
		case "passport":
			return a.passport
		case "sphere":
			return a.sphere
		case "wallet":
			return a.wallet
		}
	}
	if _, ok := statistics.QualifiedRoute(path); ok && service == "api" {
		return a.qualified
	}
	if kind, _, ok := verification.Route(path); ok && (service == "api" || kind == verification.Publisher && service == "sphere" || kind != verification.Publisher && service == "passport") {
		return a.verification
	}
	if automod.Handles(path) && (service == "api" || service == "sphere") {
		return a.automod
	}
	return nil
}
