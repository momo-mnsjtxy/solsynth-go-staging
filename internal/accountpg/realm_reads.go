// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

func (r *Repository) ListPublishersForViewer(ctx context.Context, viewer, account string) (out []accounts.Publisher, err error) {
	out = []accounts.Publisher{}
	if !authority.ValidID(account) {
		return nil, accounts.ErrInvalid
	}
	err = r.publisherRead(ctx, viewer, func(tx *sql.Tx) error {
		if _, e := lookupAccount(ctx, tx, account); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+publisherColumns+` FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id LEFT JOIN owned_publishing_settings s ON s.account_id=m.account_id WHERE m.account_id=$1 AND $2::text IS NOT NULL AND m.revoked_at IS NULL AND m.joined_at IS NOT NULL AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)) AND `+r.publisherVisible("p", "$2")+` AND `+r.publisherMember("p", "$1")+` ORDER BY (s.default_publisher_id=p.id) DESC NULLS LAST,p.created_at,p.id`, account, viewer)
		if e != nil {
			return e
		}
		for rows.Next() {
			p, e := scanPublisher(rows)
			if e != nil {
				rows.Close()
				return e
			}
			out = append(out, p)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for i := range out {
			if e = hydrate(ctx, tx, &out[i]); e != nil {
				return e
			}
		}
		return nil
	})
	return
}
