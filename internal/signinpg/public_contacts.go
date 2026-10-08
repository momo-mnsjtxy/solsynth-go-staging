// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

func (r *Repository) PublicContacts(ctx context.Context, db *sql.DB, actor string) (out []signin.Contact, e error) {
	out = []signin.Contact{}
	if r == nil || r.db == nil || r.db != db {
		return nil, authority.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, signin.ErrInput
	}
	rows, e := r.db.QueryContext(ctx, `SELECT `+contactFields+` FROM owned_authority_contacts m JOIN owned_authority_accounts a ON a.id=m.account_id LEFT JOIN owned_authority_credentials c ON c.account_id=m.credential_account_id WHERE m.account_id=$1::uuid AND NOT a.disabled AND m.deleted_at IS NULL AND m.is_public AND COALESCE(c.verified_at,m.verified_at)IS NOT NULL AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))ORDER BY m.created_at,m.id LIMIT 33`, actor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanContact(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
		if len(out) > 32 {
			return nil, signin.ErrConflict
		}
	}
	return out, rows.Err()
}
