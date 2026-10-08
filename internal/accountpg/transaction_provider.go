// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// These methods join a caller-owned mutation transaction. They never authorize
// identity: the caller must verify authority proof and lock the actor authority
// account row FIRST. The publisher lock is held until caller commit/rollback,
// serializing post writes with every publisher membership mutation. They query
// only this canonical schema; no publisher/account UUID equality is assumed.
func (r *Repository) RequirePostEditor(ctx context.Context, tx *sql.Tx, actor, publisher string) (accounts.Publisher, error) {
	if tx == nil || !authority.ValidID(actor) {
		return accounts.Publisher{}, accounts.ErrForbidden
	}
	p, e := lookupPublisher(ctx, tx, publisher, true)
	if e != nil {
		return p, e
	}
	if e = require(ctx, tx, p.ID, actor, accounts.Editor); e != nil {
		return accounts.Publisher{}, e
	}
	return p, nil
}
func (r *Repository) ResolveForPost(ctx context.Context, tx *sql.Tx, actor, selector string) (accounts.Publisher, error) {
	if tx == nil || !authority.ValidID(actor) {
		return accounts.Publisher{}, accounts.ErrForbidden
	}
	if selector == "" {
		var id string
		e := tx.QueryRowContext(ctx, `SELECT default_publisher_id::text FROM owned_publishing_settings WHERE account_id=$1`, actor).Scan(&id)
		if e == nil {
			selector = id
		} else if e != sql.ErrNoRows {
			return accounts.Publisher{}, e
		} else {
			ids, e := r.ListPostPublisherIDs(ctx, tx, actor)
			if e != nil {
				return accounts.Publisher{}, e
			}
			if len(ids) == 0 {
				return accounts.Publisher{}, accounts.ErrForbidden
			}
			if len(ids) != 1 {
				return accounts.Publisher{}, accounts.ErrAmbiguous
			}
			selector = ids[0]
		}
	}
	return r.RequirePostEditor(ctx, tx, actor, selector)
}

// ListPostPublisherIDs is an observation, never sufficient authorization for a
// later write; RequirePostEditor must run while holding the mutation transaction.
func (r *Repository) ListPostPublisherIDs(ctx context.Context, tx *sql.Tx, actor string) ([]string, error) {
	if tx == nil || !authority.ValidID(actor) {
		return nil, accounts.ErrForbidden
	}
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.account_id=$1 AND NOT a.disabled AND m.role>=$2 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=p.account_id AND NOT owner.disabled)) ORDER BY p.created_at,p.id`, actor, accounts.Editor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
