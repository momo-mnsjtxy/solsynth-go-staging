// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
)

func (r *Repository) authorizeWrite(ctx context.Context, tx *sql.Tx, actor, permission string) (authority.Principal, error) {
	if permission != "developers.bots.create" && permission != "developers.bots.delete" && permission != "developers.bots.keys.manage" {
		return r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	g, ok := r.guard.(interface {
		AuthorizeSudo(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
	})
	if !ok {
		return authority.Principal{}, authority.ErrSudoUnavailable
	}
	return g.AuthorizeSudo(ctx, r.db, tx, actor, permission)
}
