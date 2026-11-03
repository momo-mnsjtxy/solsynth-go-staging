// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"time"
)

// The persisted completed challenge, current account and current session must
// all match. No request may supply elevation methods, trust points or lifetime.
func (t *transaction) SaveSudoElevation(c signin.Challenge, p authority.Principal, now time.Time) error {
	if c.Purpose != "sudo" || !c.Completed || !authority.ValidSudoMethods(c.AuthMethods) || c.SudoSessionID != p.Session.ID || c.SudoSessionEpoch != p.Session.Epoch || c.AccountID != p.Account.ID {
		return authority.ErrInvalid
	}
	r, err := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_sudo_elevations(session_id,account_id,account_version,session_epoch,factor_policy_version,challenge_hash,auth_time,expires_at,methods,trust_points)SELECT s.id,s.account_id,s.account_version,s.epoch,c.sudo_policy_version,c.hash,$4::timestamptz,$4::timestamptz+interval '5 minutes',c.auth_methods,2 FROM owned_authority_sessions s JOIN owned_authority_accounts a ON a.id=s.account_id JOIN owned_authority_challenges c ON c.account_id=s.account_id AND c.sudo_session_id=s.id AND c.sudo_session_epoch=s.epoch WHERE s.id=$1::uuid AND s.account_id=$2::uuid AND s.epoch=$3::bigint AND s.account_version=$5::bigint AND s.type=0 AND s.category='device' AND s.parent_id IS NULL AND s.client_id='' AND s.revoked_at IS NULL AND s.expires_at>$4 AND c.hash=$6::bytea AND c.sudo_policy_version=a.factor_policy_version AND c.purpose='sudo' AND c.completed AND c.password_ok AND c.second_ok AND c.account_version=s.account_version AND c.expires_at>$4 ON CONFLICT(session_id) DO UPDATE SET factor_policy_version=EXCLUDED.factor_policy_version,account_version=EXCLUDED.account_version,session_epoch=EXCLUDED.session_epoch,challenge_hash=EXCLUDED.challenge_hash,auth_time=EXCLUDED.auth_time,expires_at=EXCLUDED.expires_at,methods=EXCLUDED.methods,trust_points=EXCLUDED.trust_points WHERE owned_authority_sudo_elevations.account_id=EXCLUDED.account_id`, p.Session.ID, p.Account.ID, p.Session.Epoch, now, p.Account.Version, c.Hash)
	if err != nil {
		return err
	}
	return one(r)
}
func (t *transaction) SaveSudoEmail(challenge, hash []byte, expires time.Time) error {
	r, err := t.tx.ExecContext(t.ctx, `INSERT INTO owned_authority_sudo_email_codes(challenge_hash,code_hash,expires_at)SELECT hash,$3::bytea,$4::timestamptz FROM owned_authority_challenges WHERE account_id=$1::uuid AND hash=$2::bytea AND purpose='sudo' AND sudo_email AND NOT completed ON CONFLICT(challenge_hash)DO UPDATE SET code_hash=EXCLUDED.code_hash,expires_at=EXCLUDED.expires_at,used=false`, t.canonical.Account().ID, challenge, hash, expires)
	if err != nil {
		return err
	}
	return one(r)
}
func (t *transaction) SudoEmail(challenge []byte) ([]byte, time.Time, bool, error) {
	var hash []byte
	var expires time.Time
	var used bool
	err := t.tx.QueryRowContext(t.ctx, `SELECT e.code_hash,e.expires_at,e.used FROM owned_authority_sudo_email_codes e JOIN owned_authority_challenges c ON c.hash=e.challenge_hash WHERE c.account_id=$1 AND c.hash=$2 AND c.purpose='sudo' FOR UPDATE OF e`, t.canonical.Account().ID, challenge).Scan(&hash, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		err = authority.ErrNotFound
	}
	return hash, expires, used, err
}
func (t *transaction) ConsumeSudoEmail(challenge, hash []byte, now time.Time) error {
	r, err := t.tx.ExecContext(t.ctx, `UPDATE owned_authority_sudo_email_codes e SET used=true FROM owned_authority_challenges c WHERE c.hash=e.challenge_hash AND c.account_id=$1 AND c.hash=$2 AND e.code_hash=$3 AND NOT e.used AND e.expires_at>$4`, t.canonical.Account().ID, challenge, hash, now)
	if err != nil {
		return err
	}
	return one(r)
}

func (t *transaction) SudoPolicyVersion() (int64, error) {
	var version int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT factor_policy_version FROM owned_authority_accounts WHERE id=$1`, t.canonical.Account().ID).Scan(&version)
	return version, err
}
