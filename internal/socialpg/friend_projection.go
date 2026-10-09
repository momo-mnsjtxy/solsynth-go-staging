// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
)

// FriendsInTransaction returns only current mutually accepted, unblocked,
// eligible accounts. The caller retains and rechecks its original authority.
// limit bounds discovery before any multi-account lock or profile projection.
func FriendsInTransaction(ctx context.Context, tx *sql.Tx, actor string, limit int) ([]string, error) {
	if tx == nil || !authority.ValidID(actor) || limit < 1 || limit > 1001 {
		return nil, social.ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, `SELECT a.id::text FROM owned_authority_accounts a WHERE a.id<>$1::uuid AND NOT a.disabled AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$1::uuid AND related_id=a.id AND status>=100) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=a.id AND related_id=$1::uuid AND status>=100) AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE ((account_id=$1::uuid AND related_id=a.id) OR(account_id=a.id AND related_id=$1::uuid)) AND status=-100) AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) ORDER BY a.id LIMIT $2::integer`, actor, limit)
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
