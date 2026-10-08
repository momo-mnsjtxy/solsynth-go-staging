// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"

	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// Create selection is observed for lock planning, then resolved and authorized
// again under the complete realm/publisher lock set.
func (r *Repository) createPublisher(ctx context.Context, tx *sql.Tx, actor, selector string, v posts.Patch) (accounts.Publisher, error) {
	id, e := r.publishers.ObservePostPublisherID(ctx, tx, actor, selector)
	if e != nil {
		return accounts.Publisher{}, e
	}
	if e = r.prelock(ctx, tx, actor, MutationScope{PublisherID: id}, v, true); e != nil {
		return accounts.Publisher{}, e
	}
	actual, e := r.publishers.ObservePostPublisherID(ctx, tx, actor, selector)
	if e != nil {
		return accounts.Publisher{}, e
	}
	if actual != id {
		return accounts.Publisher{}, posts.ErrConflict
	}
	// Use the locked ID, never take a newly resolved publisher lock out of order.
	return r.publishers.RequirePostEditor(ctx, tx, actor, id)
}
