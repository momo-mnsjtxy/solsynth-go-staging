// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"context"
	"database/sql"
	a "solsynth.local/go-migration/ports/admin/accounts"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"time"
)

// Caller holds canonical target account lock plus explicit global accounts.manage
// root-session authority, which must be revalidated after this helper's waits.
func AdministrativeMutationInTransaction(ctx context.Context, tx *sql.Tx, id string, m a.Mutation, now time.Time) (any, error) {
	if tx == nil || !authority.ValidID(id) {
		return nil, authority.ErrInvalid
	}
	contact := func(key string) (signin.Contact, error) {
		return scanContact(tx.QueryRowContext(ctx, `SELECT `+contactFields+` FROM owned_authority_contacts m LEFT JOIN owned_authority_credentials c ON c.account_id=m.credential_account_id WHERE m.id=$1::uuid AND m.account_id=$2::uuid AND m.deleted_at IS NULL`, key, id))
	}
	cancel := func() error {
		_, e := tx.ExecContext(ctx, `UPDATE owned_authority_challenges SET completed=true WHERE account_id=$1::uuid AND NOT completed`, id)
		return e
	}
	bump := func() error {
		if _, e := tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET version=version+1 WHERE id=$1::uuid`, id); e != nil {
			return e
		}
		return cancel()
	}
	switch m.Operation {
	case "password.reset":
		if m.Password == nil || m.Contact != nil || m.Public != nil {
			return nil, signin.ErrInput
		}
		hash, e := signin.HashAdministrativePassword(*m.Password)
		if e != nil {
			return nil, e
		}
		key, e := contactID()
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_factors(id,account_id,type,secret,enabled_at,created_at)VALUES($1::uuid,$2::uuid,0,$3::bytea,$4::timestamptz,$4::timestamptz)ON CONFLICT(account_id,type)DO UPDATE SET secret=EXCLUDED.secret,enabled_at=EXCLUDED.enabled_at,revision=owned_authority_factors.revision+1`, key, id, hash, now); e != nil {
			return nil, e
		}
		return nil, bump()
	case "factor.delete":
		if !authority.ValidID(m.ResourceID) {
			return nil, signin.ErrInput
		}
		var kind int
		e := tx.QueryRowContext(ctx, `SELECT type FROM owned_authority_factors WHERE id=$1::uuid AND account_id=$2::uuid FOR UPDATE`, m.ResourceID, id).Scan(&kind)
		if e != nil {
			return nil, e
		}
		if kind == 0 {
			return nil, signin.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_authority_factors WHERE id=$1::uuid AND account_id=$2::uuid`, m.ResourceID, id); e != nil {
			return nil, e
		}
		return nil, bump()
	case "session.revoke":
		if !authority.ValidID(m.ResourceID) {
			return nil, signin.ErrInput
		}
		var found string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_sessions WHERE id=$1::uuid AND account_id=$2::uuid FOR UPDATE`, m.ResourceID, id).Scan(&found); e != nil {
			return nil, e
		}
		_, e := tx.ExecContext(ctx, `WITH RECURSIVE descendants AS(SELECT id FROM owned_authority_sessions WHERE id=$1::uuid AND account_id=$2::uuid UNION ALL SELECT s.id FROM owned_authority_sessions s JOIN descendants d ON s.parent_id=d.id WHERE s.account_id=$2::uuid)UPDATE owned_authority_sessions SET revoked_at=COALESCE(revoked_at,$3::timestamptz),epoch=epoch+1,refresh_hash=NULL,previous_hash=NULL,previous_until=NULL,sealed_pair=NULL WHERE id IN(SELECT id FROM descendants)`, m.ResourceID, id, now)
		return nil, e
	case "contact.create":
		if m.Contact == nil {
			return nil, signin.ErrInput
		}
		input := *m.Contact
		if e := signin.ValidateContact(&input); e != nil {
			return nil, e
		}
		if input.Type == 0 {
			if e := lockEmails(ctx, tx, input.Content); e != nil {
				return nil, e
			}
			used, e := emailOwned(ctx, tx, input.Content, "")
			if e != nil {
				return nil, e
			}
			if used {
				return nil, signin.ErrConflict
			}
		}
		var count int
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_authority_contacts WHERE account_id=$1::uuid AND deleted_at IS NULL`, id).Scan(&count); e != nil {
			return nil, e
		}
		if count >= 32 {
			return nil, signin.ErrConflict
		}
		key, e := contactID()
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_contacts(id,account_id,type,content,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::integer,$4::text,$5::timestamptz,$5::timestamptz)`, key, id, input.Type, input.Content, now); e != nil {
			return nil, conflict(e)
		}
		return contact(key)
	}
	if !authority.ValidID(m.ResourceID) {
		return nil, signin.ErrInput
	}
	v, e := contact(m.ResourceID)
	if e != nil {
		return nil, e
	}
	if v.Type == 0 && m.Operation != "contact.primary" {
		if e = lockEmails(ctx, tx, v.Content); e != nil {
			return nil, e
		}
	}
	switch m.Operation {
	case "contact.delete":
		if v.IsPrimary {
			return nil, signin.ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_authority_contacts SET deleted_at=$3::timestamptz,updated_at=$3::timestamptz,is_public=false WHERE id=$1::uuid AND account_id=$2::uuid`, v.ID, id, now)
		if e == nil {
			_, e = tx.ExecContext(ctx, `UPDATE owned_authority_challenges SET completed=true WHERE account_id=$1::uuid AND contact_id=$2::uuid`, id, v.ID)
		}
		return nil, e
	case "contact.visibility":
		if m.Public == nil {
			return nil, signin.ErrInput
		}
		if *m.Public && v.VerifiedAt == nil {
			return nil, signin.ErrUnverified
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_authority_contacts SET is_public=$3::boolean,updated_at=$4::timestamptz WHERE id=$1::uuid AND account_id=$2::uuid`, v.ID, id, *m.Public, now)
	case "contact.verify":
		if v.Type != 0 {
			return nil, signin.ErrUnsupported
		}
		used, x := emailOwned(ctx, tx, v.Content, id)
		if x != nil {
			return nil, x
		}
		if used {
			return nil, signin.ErrConflict
		}
		if v.IsPrimary {
			_, e = tx.ExecContext(ctx, `UPDATE owned_authority_credentials SET verified_at=COALESCE(verified_at,$2::timestamptz)WHERE account_id=$1::uuid`, id, now)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE owned_authority_contacts SET verified_at=COALESCE(verified_at,$3::timestamptz),updated_at=$3::timestamptz WHERE id=$1::uuid AND account_id=$2::uuid`, v.ID, id, now)
		}
	case "contact.primary":
		if v.Type != 0 || v.VerifiedAt == nil {
			return nil, signin.ErrUnverified
		}
		if v.IsPrimary {
			return v, nil
		}
		current, x := scanContact(tx.QueryRowContext(ctx, `SELECT `+contactFields+` FROM owned_authority_contacts m JOIN owned_authority_credentials c ON c.account_id=m.credential_account_id WHERE m.account_id=$1::uuid AND m.deleted_at IS NULL`, id))
		if x != nil {
			return nil, x
		}
		// Acquire both canonical email keys in sorted order from the beginning. Target
		// account locking prevents this account's contacts changing concurrently.
		if e = lockEmails(ctx, tx, current.Content, v.Content); e != nil {
			return nil, e
		}
		used, x := emailOwned(ctx, tx, v.Content, id)
		if x != nil {
			return nil, x
		}
		if used {
			return nil, signin.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_credentials SET email=$2::text,verified_at=$3::timestamptz WHERE account_id=$1::uuid`, id, v.Content, v.VerifiedAt); e != nil {
			return nil, conflict(e)
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_contacts SET credential_account_id=NULL,content=$2::text,verified_at=$3::timestamptz,updated_at=$4::timestamptz WHERE id=$1::uuid`, current.ID, current.Content, current.VerifiedAt, now); e != nil {
			return nil, conflict(e)
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_contacts SET credential_account_id=$2::uuid,content=NULL,verified_at=NULL,updated_at=$3::timestamptz WHERE id=$1::uuid`, v.ID, id, now); e != nil {
			return nil, e
		}
		e = bump()
	default:
		return nil, signin.ErrInput
	}
	if e != nil {
		return nil, conflict(e)
	}
	return contact(v.ID)
}
