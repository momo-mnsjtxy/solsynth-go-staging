// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

// Fixed SQL uses the documented postpg aliases p/u and its parameter $1. No
// request value is interpolated. The host installs this optional provider only
// after explicitly installing the social schema.
func (r *Repository) PostBlockedSQL() string {
	return `EXISTS(SELECT 1 FROM owned_social_effective_relationships sr WHERE sr.status=-100 AND ((sr.account_id=u.account_id AND sr.related_id=NULLIF($1::text,'')::uuid) OR(sr.related_id=u.account_id AND sr.account_id=NULLIF($1::text,'')::uuid)))`
}
func (r *Repository) PostVisibilitySQL() string {
	return `(u.type=0 AND u.account_id IS NOT NULL AND p.visibility IN(1,4) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships owner_rel WHERE owner_rel.account_id=u.account_id AND owner_rel.related_id=NULLIF($1::text,'')::uuid AND owner_rel.status>=CASE WHEN p.visibility=4 THEN 200 ELSE 100 END) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships viewer_rel WHERE viewer_rel.account_id=NULLIF($1::text,'')::uuid AND viewer_rel.related_id=u.account_id AND viewer_rel.status>=100) AND NOT ` + r.PostBlockedSQL() + `)`
}
func (r *Repository) IsBlockedInTransaction(ctx context.Context, tx *sql.Tx, a, b string) (bool, error) {
	if tx == nil {
		return false, social.ErrUnavailable
	}
	if !authority.ValidID(a) || !authority.ValidID(b) {
		return false, social.ErrInvalid
	}
	return blocked(ctx, tx, a, b)
}
func (r *Repository) CanReadFriendsInTransaction(ctx context.Context, tx *sql.Tx, viewer, publisher string, close bool) (bool, error) {
	if tx == nil {
		return false, social.ErrUnavailable
	}
	if !authority.ValidID(viewer) || !authority.ValidID(publisher) {
		return false, social.ErrInvalid
	}
	minimum := social.Friend
	if close {
		minimum = social.CloseFriend
	}
	var v bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p JOIN owned_authority_accounts owner ON owner.id=p.account_id JOIN owned_authority_accounts viewer ON viewer.id=$1 WHERE p.id=$2 AND p.type=0 AND p.deleted_at IS NULL AND NOT owner.disabled AND NOT viewer.disabled AND EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE f.account_id=p.account_id AND f.related_id=$1 AND f.status>=$3) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE f.account_id=$1 AND f.related_id=p.account_id AND f.status>=100) AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE f.status=-100 AND ((f.account_id=$1 AND f.related_id=p.account_id) OR(f.account_id=p.account_id AND f.related_id=$1))))`, viewer, publisher, minimum).Scan(&v)
	return v, e
}
func (r *Repository) RealmRoleInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (social.Role, error) {
	if tx == nil {
		return 0, social.ErrUnavailable
	}
	if !authority.ValidID(actor) || !authority.ValidID(id) {
		return 0, social.ErrInvalid
	}
	v, e := r.realm(ctx, tx, id, false)
	if e != nil {
		return 0, e
	}
	m, e := requireRole(ctx, tx, v.ID, actor, social.Normal)
	if e == nil {
		if v.AccountID == actor {
			return social.Owner, nil
		}
		if m.Role == social.Owner {
			return 0, social.ErrForbidden
		}
	}
	return m.Role, e
}

// RealmInTransaction projects the canonical realm in the caller's snapshot.
// Callers separately enforce visibility or role before exposing its fields.
func (r *Repository) RealmInTransaction(ctx context.Context, tx *sql.Tx, id string) (social.Realm, error) {
	if tx == nil {
		return social.Realm{}, social.ErrUnavailable
	}
	return r.realm(ctx, tx, id, false)
}
func (r *Repository) CanChatInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	_, e := r.requirePermission(ctx, tx, actor, id, social.CanChat)
	if errors.Is(e, social.ErrForbidden) || errors.Is(e, social.ErrNotFound) {
		return false, nil
	}
	return e == nil, e
}

// Realm and RealmIDs implement the wallet's verified-identity bridge. The
// bridge revalidates proof before invocation; membership itself is never proof.
// This owned policy permits active members to view and only owners to edit a
// realm wallet. Custom legacy permission/boost policies are not inferred.
func (r *Repository) Realm(ctx context.Context, p wallet.Principal, id string, need wallet.RealmRole) error {
	if p.SessionID == "" || !authority.ValidID(p.AccountID) {
		return wallet.ErrUnauthorized
	}
	if !authority.ValidID(id) || (need != wallet.Viewer && need != wallet.Editor) {
		return wallet.ErrInvalid
	}
	e := r.read(ctx, p.AccountID, func(tx *sql.Tx) error {
		role, e := r.RealmRoleInTransaction(ctx, tx, p.AccountID, id)
		if e != nil {
			return e
		}
		if need == wallet.Editor && role != social.Owner {
			return social.ErrForbidden
		}
		return nil
	})
	return walletRealmError(e)
}
func walletRealmError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, social.ErrForbidden) || errors.Is(e, social.ErrNotFound) {
		return wallet.ErrForbidden
	}
	return wallet.ErrUnavailable
}
func (r *Repository) RealmIDs(ctx context.Context, p wallet.Principal) (out []string, err error) {
	out = []string{}
	if p.SessionID == "" || !authority.ValidID(p.AccountID) {
		return nil, wallet.ErrUnauthorized
	}
	err = r.read(ctx, p.AccountID, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT r.id::text FROM owned_realms r JOIN owned_realm_members m ON m.realm_id=r.id JOIN owned_authority_accounts a ON a.id=r.account_id WHERE m.account_id=$1 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL AND NOT a.disabled ORDER BY r.id`, p.AccountID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, walletRealmError(err)
}

var _ wallet.RealmAuthority = (*Repository)(nil)

// RealmRoleForWriteInTransaction retains the realm lifecycle/membership lock
// through the caller's commit. The caller must first authenticate and lock the
// actor with its transaction authority guard. ReadCommitted ensures a waiter
// observes membership revocation committed before it acquired the realm lock.
func (r *Repository) RealmRoleForWriteInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (social.Role, error) {
	if tx == nil {
		return 0, social.ErrUnavailable
	}
	if !authority.ValidID(actor) || !authority.ValidID(id) {
		return 0, social.ErrInvalid
	}
	var isolation string
	if e := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); e != nil {
		return 0, e
	}
	if isolation != "read committed" {
		return 0, social.ErrUnavailable
	}
	v, e := r.realm(ctx, tx, id, true)
	if e != nil {
		return 0, e
	}
	m, e := requireRole(ctx, tx, v.ID, actor, social.Normal)
	if e == nil {
		if v.AccountID == actor {
			return social.Owner, nil
		}
		if m.Role == social.Owner {
			return 0, social.ErrForbidden
		}
	}
	return m.Role, e
}
