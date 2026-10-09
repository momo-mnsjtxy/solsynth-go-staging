// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
)

// GrantExamRewardInTransaction joins the exam's canonical account transaction.
// It verifies the trusted grading receipt before recording XP in the one existing
// progression ledger. No public route can supply arbitrary reward amounts.
func (r *Repository) GrantExamRewardInTransaction(ctx context.Context, tx *sql.Tx, actor, test, attempt string, amount int64) error {
	if r == nil || tx == nil || !authority.ValidID(actor) || !authority.ValidID(test) || !authority.ValidID(attempt) || amount < 1 || amount > 1000000000 {
		return progression.ErrInvalid
	}
	var expectedAttempt, state string
	var expectedAmount int64
	if e := tx.QueryRowContext(ctx, `SELECT attempt_id::text,amount,state FROM owned_exam_rewards WHERE account_id=$1 AND test_id=$2 FOR UPDATE`, actor, test).Scan(&expectedAttempt, &expectedAmount, &state); e != nil {
		return e
	}
	if expectedAttempt != attempt || expectedAmount != amount || (state != "pending" && state != "applied") {
		return progression.ErrInvalid
	}
	var active bool
	if e := tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&active); e != nil {
		return e
	}
	if !active {
		return authority.ErrDisabled
	}
	id, e := newID()
	if e != nil {
		return e
	}
	token := "exam:" + actor + ":" + test
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_experience(id,account_id,reason_type,reason,delta,reward_token,created_at)VALUES($1,$2,'exam',$3,$4,$5,clock_timestamp()) ON CONFLICT(reward_token) DO NOTHING`, id, actor, test, amount, token); e != nil {
		return e
	}
	var account, reason string
	var credited int64
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text,reason,delta FROM owned_progression_experience WHERE reward_token=$1`, token).Scan(&account, &reason, &credited); e != nil {
		return e
	}
	if account != actor || reason != test || credited != amount {
		return progression.ErrInvalid
	}
	return nil
}
