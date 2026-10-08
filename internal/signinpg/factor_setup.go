// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

func (t *transaction) FactorSetup() (signin.FactorSetup, error) {
	var v signin.FactorSetup
	var recovery, totp sql.NullString
	var rv, tv sql.NullInt64
	e := t.tx.QueryRowContext(t.ctx, `SELECT session_id::text,account_version,authorization_epoch,auth_time,expires_at,policy_hash,recovery_id::text,recovery_revision,totp_id::text,totp_revision,state FROM owned_authority_factor_setups WHERE account_id=$1::uuid FOR UPDATE`, t.canonical.Account().ID).Scan(&v.SessionID, &v.AccountVersion, &v.AuthorizationEpoch, &v.AuthTime, &v.ExpiresAt, &v.PolicyHash, &recovery, &rv, &totp, &tv, &v.State)
	if errors.Is(e, sql.ErrNoRows) {
		return v, authority.ErrNotFound
	}
	v.RecoveryID = recovery.String
	v.RecoveryRevision = rv.Int64
	v.TOTPID = totp.String
	v.TOTPRevision = tv.Int64
	return v, e
}
func (t *transaction) SaveFactorSetup(v signin.FactorSetup) error {
	_, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_factor_setups(account_id,session_id,account_version,authorization_epoch,auth_time,expires_at,policy_hash,recovery_id,recovery_revision,totp_id,totp_revision,state)VALUES($1::uuid,$2::uuid,$3::bigint,$4::bigint,$5::timestamptz,$6::timestamptz,$7::text,NULLIF($8::text,'')::uuid,NULLIF($9::bigint,0),NULLIF($10::text,'')::uuid,NULLIF($11::bigint,0),$12::text)ON CONFLICT(account_id)DO UPDATE SET session_id=EXCLUDED.session_id,account_version=EXCLUDED.account_version,authorization_epoch=EXCLUDED.authorization_epoch,auth_time=EXCLUDED.auth_time,expires_at=EXCLUDED.expires_at,policy_hash=EXCLUDED.policy_hash,recovery_id=EXCLUDED.recovery_id,recovery_revision=EXCLUDED.recovery_revision,totp_id=EXCLUDED.totp_id,totp_revision=EXCLUDED.totp_revision,state=EXCLUDED.state`, t.canonical.Account().ID, v.SessionID, v.AccountVersion, v.AuthorizationEpoch, v.AuthTime, v.ExpiresAt, v.PolicyHash, v.RecoveryID, v.RecoveryRevision, v.TOTPID, v.TOTPRevision, v.State)
	return e
}
func (t *transaction) PendingEnrollment(id string) (signin.PendingEnrollment, error) {
	var v signin.PendingEnrollment
	e := t.tx.QueryRowContext(t.ctx, `SELECT factor_id::text,factor_revision,account_version,policy_hash,pending_until,valid FROM owned_authority_pending_enrollments WHERE account_id=$1::uuid AND factor_id=$2::uuid FOR UPDATE`, t.canonical.Account().ID, id).Scan(&v.FactorID, &v.FactorRevision, &v.AccountVersion, &v.PolicyHash, &v.PendingUntil, &v.Valid)
	if errors.Is(e, sql.ErrNoRows) {
		e = authority.ErrNotFound
	}
	return v, e
}
func (t *transaction) SavePendingEnrollment(v signin.PendingEnrollment) error {
	_, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_pending_enrollments(factor_id,account_id,factor_revision,account_version,policy_hash,pending_until,valid)VALUES($1::uuid,$2::uuid,$3::bigint,$4::bigint,$5::text,$6::timestamptz,$7::boolean)ON CONFLICT(account_id)DO UPDATE SET factor_id=EXCLUDED.factor_id,factor_revision=EXCLUDED.factor_revision,account_version=EXCLUDED.account_version,policy_hash=EXCLUDED.policy_hash,pending_until=EXCLUDED.pending_until,valid=EXCLUDED.valid WHERE owned_authority_pending_enrollments.account_id=EXCLUDED.account_id`, v.FactorID, t.canonical.Account().ID, v.FactorRevision, v.AccountVersion, v.PolicyHash, v.PendingUntil, v.Valid)
	return e
}
func (t *transaction) CancelFactorSetup() error {
	if _, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_factor_setups SET state='cancelled' WHERE account_id=$1::uuid AND state IN('recovery','pending')`, t.canonical.Account().ID); e != nil {
		return e
	}
	_, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_pending_enrollments SET valid=false WHERE account_id=$1::uuid AND valid`, t.canonical.Account().ID)
	return e
}
func (t *transaction) SetupPaymentVersion() (int64, error) {
	var exists bool
	if e := t.tx.QueryRowContext(t.ctx, `SELECT to_regclass('wallet_v1.payment_policies') IS NOT NULL`).Scan(&exists); e != nil {
		return 0, e
	}
	if !exists {
		return 0, nil
	}
	// All canonical payment-policy writers take this same account lock first.
	var v int64
	e := t.tx.QueryRowContext(t.ctx, `SELECT version FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR SHARE`, t.canonical.Account().ID).Scan(&v)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, nil
	}
	return v, e
}

// Include durable history owned by the canonical account and its current groups.
// Unlike surviving row IDs/xmin, these versions retain insert/delete and restore
// evidence, including group grants changed while a membership was being added.
func (t *transaction) SetupPolicyIdentity() (string, error) {
	var v string
	e := t.tx.QueryRowContext(t.ctx, `SELECT jsonb_build_array(a.factor_policy_version,
  (SELECT COALESCE(jsonb_agg(jsonb_build_array(g.id,g.factor_policy_version)ORDER BY g.id),'[]'::jsonb)
   FROM owned_authority_groups g JOIN owned_authority_group_members m ON m.group_id=g.id WHERE m.account_id=a.id)
 )::text FROM owned_authority_accounts a WHERE a.id=$1::uuid`, t.canonical.Account().ID).Scan(&v)
	return v, e
}
