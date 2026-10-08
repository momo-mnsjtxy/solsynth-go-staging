// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/collectionadminpg"
	"solsynth.local/go-migration/internal/domaintrustpg"
	"solsynth.local/go-migration/ports/admin/collections"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/domaintrust"
	"time"
)

type domainCollectionModules struct {
	domains                    *domaintrustpg.Repository
	collections                *collectionadminpg.Repository
	domainHTTP, collectionHTTP http.Handler
}

func domainCollectionsFor(db *sql.DB, g *authoritypg.TransactionGuard, v *authority.Verifier, now func() time.Time) (out domainCollectionModules, err error) {
	var dr domaintrust.Repository
	var cr collections.Repository
	if g != nil {
		out.domains, err = domaintrustpg.New(db, g, domaintrustpg.Config{Now: now})
		if err != nil {
			return
		}
		out.collections, err = collectionadminpg.New(db, g, now)
		if err != nil {
			return
		}
		dr = out.domains
		cr = out.collections
	}
	auth := func(r *http.Request, required bool) (string, error) {
		return (postIdentity{identity{v}}).Authenticate(r)
	}
	wrap := func(h http.Handler) http.Handler { return bindIdentity(v, bindTransactionAuthority(g, h)) }
	out.domainHTTP = wrap(domaintrust.NewHandler(dr, auth))
	out.collectionHTTP = wrap(collections.NewHandler(cr, auth))
	return
}
func (m domainCollectionModules) ready(ctx context.Context) error {
	if m.domains != nil {
		if e := m.domains.Ready(ctx); e != nil {
			return e
		}
	}
	if m.collections != nil {
		return m.collections.Ready(ctx)
	}
	return nil
}
func (m domainCollectionModules) route(path string) http.Handler {
	if domaintrust.Handles(path) {
		return m.domainHTTP
	}
	if collections.Handles(path) {
		return m.collectionHTTP
	}
	return nil
}
