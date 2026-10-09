// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

// These read-only projections join an already-authorized administrative
// transaction. The caller must hold the canonical target account lock and
// revalidate its original proof and explicit global accounts.view grant.
func AdministrativeContactsInTransaction(ctx context.Context, tx *sql.Tx, id string) (out []signin.Contact, e error) {
	out = []signin.Contact{}
	if tx == nil || !authority.ValidID(id) {
		return nil, authority.ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+contactFields+` FROM owned_authority_contacts m LEFT JOIN owned_authority_credentials c ON c.account_id=m.credential_account_id WHERE m.account_id=$1 AND m.deleted_at IS NULL ORDER BY m.credential_account_id IS NOT NULL DESC,m.created_at,m.id LIMIT 33`, id)
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
	}
	if len(out) > 32 {
		return nil, signin.ErrConflict
	}
	return out, rows.Err()
}

func AdministrativeFactorsInTransaction(ctx context.Context, tx *sql.Tx, id string) (out []signin.FactorSummary, e error) {
	out = []signin.FactorSummary{}
	if tx == nil || !authority.ValidID(id) {
		return nil, authority.ErrInvalid
	}
	// Secret values never cross the SQL boundary. Canonical type 5 is recovery,
	// NOT the excluded upstream enum's passkey; no unlicensed enum is imported.
	rows, e := tx.QueryContext(ctx, `SELECT id::text,type,secret IS NOT NULL AND octet_length(secret)>0,created_at,enabled_at FROM owned_authority_factors WHERE account_id=$1 ORDER BY type,id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		v := signin.FactorSummary{Type: "unspecified", Trustworthy: 1}
		var kind int
		if e = rows.Scan(&v.ID, &kind, &v.HasSecret, &v.CreatedAt, &v.EnabledAt); e != nil {
			return nil, e
		}
		switch kind {
		case 0:
			v.Type = "password"
		case 3:
			v.Type = "timed_code"
		case 4:
			v.Type = "pin_code"
			v.Trustworthy = 0
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
