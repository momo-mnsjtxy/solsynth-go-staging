// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/connectionpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/connections"
)

type accountConnectionModules struct {
	repo    *connectionpg.Repository
	handler http.Handler
}

func accountConnectionsFor(db *sql.DB, g *authoritypg.TransactionGuard, v *authority.Verifier, c *connectionpg.Config) (out accountConnectionModules, e error) {
	out.handler = (&connections.Service{}).Handler()
	if c == nil {
		return
	}
	out.repo, e = connectionpg.New(db, g, *c)
	if e != nil {
		return
	}
	out.handler = (&connections.Service{Repository: out.repo, Authority: v, Bind: g.Bind}).Handler()
	return
}
func (a accountConnectionModules) ready(ctx context.Context) error {
	if a.repo == nil {
		return nil
	}
	return a.repo.Ready(ctx)
}
func (a accountConnectionModules) processDue(ctx context.Context, limit int) (int, error) {
	if a.repo == nil {
		return 0, nil
	}
	return a.repo.ProcessDue(ctx, limit)
}
