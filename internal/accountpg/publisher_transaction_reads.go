// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// VisiblePublisherInTransaction projects current canonical publisher/realm
// visibility in the caller's snapshot. It grants no mutation authority.
func (r *Repository) VisiblePublisherInTransaction(ctx context.Context, tx *sql.Tx, actor, ref string) (accounts.Publisher, error) {
	if r == nil || r.db == nil || tx == nil {
		return accounts.Publisher{}, accounts.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return accounts.Publisher{}, accounts.ErrForbidden
	}
	return r.visiblePublisher(ctx, tx, actor, ref)
}

// CanEditPublisherInTransaction is only a current membership observation. A
// writer must hold canonical authority and realm/publisher locks and revalidate.
func (r *Repository) CanEditPublisherInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	if r == nil || r.db == nil || tx == nil {
		return false, accounts.ErrUnavailable
	}
	if actor == "" {
		return false, nil
	}
	if !authority.ValidID(actor) || !authority.ValidID(id) {
		return false, accounts.ErrForbidden
	}
	e := r.require(ctx, tx, id, actor, accounts.Editor)
	if errors.Is(e, accounts.ErrForbidden) || errors.Is(e, accounts.ErrNotFound) {
		return false, nil
	}
	return e == nil, e
}
