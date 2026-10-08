// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// ObservePostPublisherID is only a lock-planning observation. RequirePostEditor
// must resolve current permission after the planned realm/publisher locks.
func (r *Repository) ObservePostPublisherID(ctx context.Context, tx *sql.Tx, actor, selector string) (string, error) {
	if tx == nil || !authority.ValidID(actor) {
		return "", accounts.ErrForbidden
	}
	if selector == "" {
		e := tx.QueryRowContext(ctx, `SELECT default_publisher_id::text FROM owned_publishing_settings WHERE account_id=$1`, actor).Scan(&selector)
		if errors.Is(e, sql.ErrNoRows) {
			ids, e := r.ListPostPublisherIDs(ctx, tx, actor)
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
