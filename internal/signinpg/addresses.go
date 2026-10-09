// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"solsynth.local/go-migration/ports/authority/signin"
	"time"
)

func (t *transaction) UpdateAddress(id string, input signin.ContactInput, now time.Time) (out signin.Contact, e error) {
	if input.Type != signin.AddressContactType {
		return out, signin.ErrInput
	}
	if e = signin.ValidateContact(&input); e != nil {
		return
	}
	out, e = t.Contact(id)
	if e != nil {
		return
	}
	if out.Type != signin.AddressContactType || out.IsPrimary {
		return out, signin.ErrInput
	}
	if out.Content == input.Content {
		if e = t.AuthorizeContacts(); e != nil {
			return signin.Contact{}, e
		}
		return out, nil
	}
	result, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_contacts SET content=$3,updated_at=$4,revision=revision+1,is_public=false WHERE id=$1 AND account_id=$2 AND type=2 AND credential_account_id IS NULL AND deleted_at IS NULL`, id, t.canonical.Account().ID, input.Content, now)
	if e != nil {
		return signin.Contact{}, conflict(e)
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return signin.Contact{}, signin.ErrConflict
	}
	if e = t.AuthorizeContacts(); e != nil {
		return signin.Contact{}, e
	}
	if e = t.contactAudit(id, "address.update", now); e != nil {
		return signin.Contact{}, e
	}
	return t.Contact(id)
}
