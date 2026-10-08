// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// The optional adapter joins the already authorized profile transaction. It
// owns references only; canonical media remains the sole file/byte authority.
type ProfileMediaStore interface {
	Apply(context.Context, *sql.Tx, string, accounts.ProfilePatch) error
	Load(context.Context, *sql.Tx, string, *accounts.Account) error
}

func (r *Repository) WithProfileMedia(p ProfileMediaStore) *Repository { r.profileMedia = p; return r }

// Authenticated public reads retain their original proof while loading the same
// deliberately public profile. Self-only fields use LookupSelf, never this path.
func (r *Repository) LookupAccountForViewer(ctx context.Context, viewer, ref string) (out accounts.Account, e error) {
	if r == nil || r.db == nil {
		return out, accounts.ErrUnavailable
	}
	if viewer != "" && !authority.ValidID(viewer) {
		return out, accounts.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if viewer != "" && r.requireAuthority {
		if r.guard == nil {
			return out, accounts.ErrUnavailable
		}
		if _, e = r.guard.Authenticate(ctx, r.db, tx, viewer); e != nil {
			return out, e
		}
	}
	out, e = lookupAccount(ctx, tx, ref)
	if e != nil {
		return accounts.Account{}, e
	}
	if r.profileMedia != nil {
		if e = r.profileMedia.Load(ctx, tx, viewer, &out); e != nil {
			return accounts.Account{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return accounts.Account{}, e
	}
	if r.contacts != nil {
		out.Contacts, e = r.contacts.PublicContacts(ctx, r.db, out.ID)
	}
	if e != nil {
		return accounts.Account{}, e
	}
	return out, nil
}
