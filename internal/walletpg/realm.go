// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/wallet/accounts"
)

// RealmAuthority uses independently provisioned grants and database time.
// It never infers a grant from a wallet's existence or from a request header.
type RealmAuthority struct{ db *sql.DB }

func NewRealmAuthority(db *sql.DB) *RealmAuthority { return &RealmAuthority{db: db} }
func (r *RealmAuthority) Realm(ctx context.Context, p accounts.Principal, id string, role accounts.RealmRole) error {
	if r == nil || r.db == nil {
		return accounts.ErrUnavailable
	}
	if !accounts.ValidID(p.AccountID) || p.SessionID == "" {
		return accounts.ErrUnauthorized
	}
	if !accounts.ValidID(id) || (role != accounts.Viewer && role != accounts.Editor) {
		return accounts.ErrInvalid
	}
	var actual string
	e := r.db.QueryRowContext(ctx, `SELECT role FROM wallet_v1.realm_grants WHERE account_id=$1 AND realm_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>statement_timestamp())`, p.AccountID, id).Scan(&actual)
	if errors.Is(e, sql.ErrNoRows) {
		return accounts.ErrForbidden
	}
	if e != nil {
		return e
	}
	if actual != "editor" && (actual != "viewer" || role != accounts.Viewer) {
		return accounts.ErrForbidden
	}
	return nil
}
func (r *RealmAuthority) RealmIDs(ctx context.Context, p accounts.Principal) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, accounts.ErrUnavailable
	}
	if !accounts.ValidID(p.AccountID) || p.SessionID == "" {
		return nil, accounts.ErrUnauthorized
	}
	rows, e := r.db.QueryContext(ctx, `SELECT realm_id::text FROM wallet_v1.realm_grants WHERE account_id=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>statement_timestamp()) AND role IN ('viewer','editor') ORDER BY realm_id`, p.AccountID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
