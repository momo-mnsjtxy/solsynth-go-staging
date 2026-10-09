// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/realmboostpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
)

type realmBoostModules struct {
	repo    *realmboostpg.Repository
	handler http.Handler
}

func realmBoostsFor(db *sql.DB, g *authoritypg.TransactionGuard, v *authority.Verifier, checkout *commercepg.Repository, social *socialpg.Repository, moderation *moderationpg.Repository, cfg *realmboostpg.Config) (out realmBoostModules, e error) {
	var repo b.Repository
	if cfg != nil {
		out.repo, e = realmboostpg.New(db, g, checkout, *cfg)
		if e != nil {
			return
		}
		repo = out.repo
		checkout.WithRealmBoosts(out.repo)
		social.WithBoosts(out.repo)
		if moderation != nil {
			moderation.WithRealmBoosts(out.repo)
		}
	}
	auth := func(r *http.Request, required bool) (string, error) {
		if !required {
			if p, ok := r.Context().Value(principalKey{}).(authority.Principal); !ok || p.Account.ID == "" {
				return "", nil
			}
		}
		return (postIdentity{identity{v}}).Authenticate(r)
	}
	out.handler = bindIdentity(v, bindTransactionAuthority(g, b.NewHandler(repo, auth)))
	return
}
func (m realmBoostModules) ready(ctx context.Context) error {
	if m.repo != nil {
		return m.repo.Ready(ctx)
	}
	return nil
}
