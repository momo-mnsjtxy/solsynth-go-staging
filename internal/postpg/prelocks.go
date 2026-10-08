// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"errors"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// observePublisher mirrors the canonical selector rules without taking a lock
// or granting permission. It is used only to plan an association's lock order;
// both resolution and RequirePostEditor run again under the acquired locks.
func (r *Repository) observePublisher(ctx context.Context, tx *sql.Tx, actor, selector string) (string, error) {
	if selector == "" {
		e := tx.QueryRowContext(ctx, `SELECT default_publisher_id::text FROM owned_publishing_settings WHERE account_id=$1`, actor).Scan(&selector)
		if errors.Is(e, sql.ErrNoRows) {
			ids, e := r.publishers.ListPostPublisherIDs(ctx, tx, actor)
			if e != nil {
				return "", e
			}
			if len(ids) == 0 {
				return "", accounts.ErrForbidden
			}
			if len(ids) != 1 {
				return "", accounts.ErrAmbiguous
			}
			selector = ids[0]
		} else if e != nil {
			return "", e
		}
	}
	if len(selector) > 256 {
		return "", accounts.ErrNotFound
	}
	match := `lower(p.name)=lower($1)`
	if authority.ValidID(selector) {
		match = `p.id=$1::uuid`
	}
	var id string
	e := tx.QueryRowContext(ctx, `SELECT p.id::text FROM owned_publishers p WHERE p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)) AND `+match, selector).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		e = accounts.ErrNotFound
	}
	return id, e
}

func (r *Repository) createPublisher(ctx context.Context, tx *sql.Tx, actor, selector string, v posts.Patch) (accounts.Publisher, error) {
	locker, ok := r.associations.(AssociationPrelocker)
	if !ok {
		return r.publishers.ResolveForPost(ctx, tx, actor, selector)
	}
	id, e := r.observePublisher(ctx, tx, actor, selector)
	if e != nil {
		return accounts.Publisher{}, e
	}
	if e = locker.Prelock(ctx, tx, actor, id, v, true); e != nil {
		return accounts.Publisher{}, e
	}
	actual, e := r.observePublisher(ctx, tx, actor, selector)
	if e != nil {
		return accounts.Publisher{}, e
	}
	if actual != id {
		return accounts.Publisher{}, posts.ErrConflict
	}
	// Use the locked ID, never take a newly resolved publisher lock out of order.
	return r.publishers.RequirePostEditor(ctx, tx, actor, id)
}
