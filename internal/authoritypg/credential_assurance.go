// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

func (t *transaction) SaveCodeEvidence(hash []byte, v authority.CredentialEvidence) error {
	if t.readOnly {
		return authority.ErrForbidden
	}
	if len(hash) != 32 || !authority.ValidCredentialMethods(v.Methods) {
		return authority.ErrInvalid
	}
	b, e := json.Marshal(v.Methods)
	if e != nil {
		return e
	}
	r, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_code_evidence(code_hash,auth_time,methods)SELECT hash,$3::timestamptz,$4::jsonb FROM owned_authority_codes WHERE hash=$1::bytea AND account_id=$2::uuid AND account_version=$5::bigint AND NOT used`, hash, t.account.ID, v.AuthTime, b, t.account.Version)
	if e != nil {
		return e
	}
	return assuranceOne(r)
}
func (t *transaction) CodeEvidence(hash []byte) (*authority.CredentialEvidence, error) {
	var v authority.CredentialEvidence
	var methods []byte
	e := t.tx.QueryRowContext(t.ctx, `SELECT e.auth_time,e.methods FROM owned_authority_code_evidence e JOIN owned_authority_codes c ON c.hash=e.code_hash WHERE c.account_id=$1::uuid AND c.hash=$2::bytea`, t.account.ID, hash).Scan(&v.AuthTime, &methods)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(methods, &v.Methods); e != nil {
		return nil, e
	}
	return &v, nil
}
func (t *transaction) SaveSessionAssurance(s authority.Session, hash []byte, v authority.CredentialEvidence, now time.Time) error {
	if t.readOnly {
		return authority.ErrForbidden
	}
	if s.AccountID != t.account.ID || s.AccountVersion != t.account.Version || s.Type != 0 || s.ParentID != "" || s.ClientID != "" || s.Epoch != 1 || !authority.ValidCredentialMethods(v.Methods) || v.AuthTime.After(now) || now.Sub(v.AuthTime) >= 5*time.Minute {
		return authority.ErrInvalid
	}
	r, e := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_credential_assurances(session_id,account_id,code_hash,account_version,session_epoch,auth_time,expires_at,methods)SELECT s.id,s.account_id,e.code_hash,s.account_version,s.epoch,e.auth_time,e.auth_time+interval '5 minutes',e.methods FROM owned_authority_sessions s JOIN owned_authority_codes c ON c.account_id=s.account_id AND c.hash=$2::bytea AND NOT c.used JOIN owned_authority_code_evidence e ON e.code_hash=c.hash WHERE s.id=$1::uuid AND s.account_id=$3::uuid AND s.account_version=$4::bigint AND s.type=0 AND s.parent_id IS NULL AND s.client_id='' AND s.epoch=1`, s.ID, hash, t.account.ID, t.account.Version)
	if e != nil {
		return e
	}
	return assuranceOne(r)
}
func (t *transaction) SessionAssurance(id string) (authority.CredentialAssurance, error) {
	var v authority.CredentialAssurance
	var methods []byte
	var action, target, config sql.NullString
	var policy sql.NullInt64
	e := t.tx.QueryRowContext(t.ctx, `SELECT session_id::text,account_version,session_epoch,auth_time,expires_at,methods,consumed_at,action,target_id::text,policy_version,config_id FROM owned_authority_credential_assurances WHERE account_id=$1::uuid AND session_id=$2::uuid FOR UPDATE`, t.account.ID, id).Scan(&v.SessionID, &v.AccountVersion, &v.SessionEpoch, &v.AuthTime, &v.ExpiresAt, &methods, &v.ConsumedAt, &action, &target, &policy, &config)
	if errors.Is(e, sql.ErrNoRows) {
		return v, authority.ErrNotFound
	}
	if e != nil {
		return v, e
	}
	v.Action = action.String
	v.TargetID = target.String
	v.ConfigID = config.String
	if policy.Valid {
		v.PolicyVersion = &policy.Int64
	}
	e = json.Unmarshal(methods, &v.Methods)
	return v, e
}
func (t *transaction) ConsumeAssurance(id string, epoch, version int64, action, target string, policy int64, config string, now time.Time) error {
	if t.readOnly {
		return authority.ErrForbidden
	}
	if len(config) > 64 || policy < 0 || version != t.account.Version || !authority.ValidID(target) || !(action == "create" || action == "enable" || action == "disable" || action == "delete") {
		return authority.ErrInvalid
	}
	r, e := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_credential_assurances SET consumed_at=$5::timestamptz,action=$6::text,target_id=$7::uuid,policy_version=$8::bigint,config_id=$9::text WHERE account_id=$1::uuid AND session_id=$2::uuid AND session_epoch=$3::bigint AND account_version=$4::bigint AND auth_time<=$5::timestamptz AND expires_at>$5::timestamptz AND consumed_at IS NULL`, t.account.ID, id, epoch, version, now, action, target, policy, config)
	if e != nil {
		return e
	}
	return assuranceOne(r)
}
func assuranceOne(r sql.Result) error {
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return authority.ErrInvalid
	}
	return nil
}
