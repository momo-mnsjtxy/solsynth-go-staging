// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

// CompleteActivation is a trusted in-process policy sink, never an HTTP entry
// point. Eligibility must have been established by the joining ticket adapter.
// It uses the same canonical account lock as policy/revocation writers. It does
// not overwrite deny rules, change account/session generations, or issue tokens.
func (r *Repository) CompleteActivation(ctx context.Context, db *sql.DB, tx *sql.Tx, account, policyID string, policy []byte, grants []string, now time.Time) error {
	if r == nil || r.db != db || tx == nil || !authority.ValidID(account) || len(policyID) < 1 || len(policyID) > 128 || !json.Valid(policy) {
		return authority.ErrInvalid
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(policy, &object) != nil || object == nil {
		return authority.ErrInvalid
	}
	seen := map[string]bool{}
	for _, key := range grants {
		if !authority.MemberActivationPermission(key) || seen[key] {
			return authority.ErrInvalid
		}
		seen[key] = true
	}
	var disabled bool
	if e := tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, account).Scan(&disabled); e != nil {
		return e
	}
	if disabled {
		return authority.ErrForbidden
	}
	var restricted bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_punishments WHERE account_id=$1 AND kind IN('disable_account','block_login')AND(starts_at IS NULL OR starts_at<=$2)AND(expires_at IS NULL OR expires_at>$2))`, account, now).Scan(&restricted); e != nil {
		return e
	}
	if restricted {
		return authority.ErrForbidden
	}
	digest := sha256.Sum256(policy)
	res, e := tx.ExecContext(ctx, `INSERT INTO owned_authority_activations(account_id,activated_at,policy_id,policy,policy_sha256)VALUES($1,$2,$3,$4,$5)ON CONFLICT(account_id)DO NOTHING`, account, now, policyID, policy, hex.EncodeToString(digest[:]))
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil || n == 0 {
		return e
	}
	for _, key := range grants {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_grants(account_id,pattern)VALUES($1,$2)`, account, key); e != nil {
			return e
		}
	}
	return nil
}
