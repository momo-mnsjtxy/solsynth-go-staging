// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/realmboostpg"
	"solsynth.local/go-migration/internal/realmmemberpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
)

type realmMemberModules struct {
	repo    *realmmemberpg.Repository
	handler http.Handler
}

func realmMembersFor(db *sql.DB, g *authoritypg.TransactionGuard, v *authority.Verifier, boosts *realmboostpg.Repository, social *socialpg.Repository, cfg *realmmemberpg.Config) (out realmMemberModules, e error) {
	var repo m.Repository
	if cfg != nil {
		if boosts == nil {
			return out, m.ErrUnavailable
		}
		out.repo, e = realmmemberpg.New(db, g, boosts, social, *cfg)
		if e != nil {
			return
		}
		repo = out.repo
		social.WithMemberDetails(out.repo)
	}
	auth := func(r *http.Request, required bool) (string, error) {
		if !required {
			if p, ok := r.Context().Value(principalKey{}).(authority.Principal); !ok || p.Account.ID == "" {
				return "", nil
			}
		}
		return (postIdentity{identity{v}}).Authenticate(r)
	}
	out.handler = bindIdentity(v, bindTransactionAuthority(g, m.NewHandler(repo, auth)))
	return
}
func (m realmMemberModules) ready(ctx context.Context) error {
	if m.repo != nil {
		return m.repo.Ready(ctx)
	}
	return nil
}
