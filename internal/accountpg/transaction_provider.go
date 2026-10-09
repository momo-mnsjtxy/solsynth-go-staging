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
	p, e := r.lockPublisher(ctx, tx, actor, publisher)
	if e != nil {
		return p, e
	}
	if e = r.require(ctx, tx, p.ID, actor, accounts.Editor); e != nil {
		return accounts.Publisher{}, e
	}
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p WHERE p.id=$1 AND `+r.activePublishingOwner("p")+`)`, p.ID).Scan(&active); e != nil {
		return accounts.Publisher{}, e
	}
	if !active {
		return accounts.Publisher{}, accounts.ErrForbidden
	}
	return p, nil
}
func (r *Repository) ResolveForPost(ctx context.Context, tx *sql.Tx, actor, selector string) (accounts.Publisher, error) {
	if tx == nil || !authority.ValidID(actor) {
		return accounts.Publisher{}, accounts.ErrForbidden
	}
	id, e := r.ObservePostPublisherID(ctx, tx, actor, selector)
	if e != nil {
		return accounts.Publisher{}, e
	}
	selector = id
	return r.RequirePostEditor(ctx, tx, actor, selector)
}

// ListPostPublisherIDs is an observation, never sufficient authorization for a
// later write; RequirePostEditor must run while holding the mutation transaction.
func (r *Repository) ListPostPublisherIDs(ctx context.Context, tx *sql.Tx, actor string) ([]string, error) {
	if tx == nil || !authority.ValidID(actor) {
		return nil, accounts.ErrForbidden
	}
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.account_id=$1 AND NOT a.disabled AND m.role>=$2 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND p.deleted_at IS NULL AND `+r.publisherMember("p", "$1")+` AND `+r.publisherVisible("p", "$1")+` AND `+r.activePublishingOwner("p")+` ORDER BY p.created_at,p.id`, actor, accounts.Editor)
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
