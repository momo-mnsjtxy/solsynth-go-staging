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
	return r.ObservePublishingPublisherID(ctx, tx, actor, selector, false)
}
func (r *Repository) ObservePublishingPublisherID(ctx context.Context, tx *sql.Tx, actor, selector string, reply bool) (string, error) {
	if tx == nil || !authority.ValidID(actor) {
		return "", accounts.ErrForbidden
	}
	if selector == "" {
		column := "default_publisher_id"
		if reply {
			column = "default_reply_publisher_id"
		}
		e := tx.QueryRowContext(ctx, `SELECT p.id::text FROM owned_publishing_settings s JOIN owned_publishers p ON p.id=s.`+column+` JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=s.account_id WHERE s.account_id=$1 AND p.deleted_at IS NULL AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=50 AND `+r.publisherMember("p", "$1")+` AND `+r.publisherVisible("p", "$1")+` AND `+r.activePublishingOwner("p"), actor).Scan(&selector)
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
	match := PublisherNamePredicate("p", "$1")
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
