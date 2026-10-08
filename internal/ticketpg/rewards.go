// SPDX-License-Identifier: AGPL-3.0-only
package ticketpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/passport/tickets"
)

// RetryPendingRewards applies committed grading receipts through the configured
// in-process progression sink. It never regrades an attempt or creates a claim.
// Candidates are read first; each transaction locks account then receipt, matching
// submit/review. One failed sink rolls back its work and does not starve the batch.
func (r *Repository) RetryPendingRewards(ctx context.Context, limit int) (int, error) {
	if r == nil || r.db == nil || limit < 1 || limit > 1000 {
		return 0, tickets.ErrInvalid
	}
	if r.cfg.Rewards == nil {
		return 0, nil
	}
	rows, e := r.db.QueryContext(ctx, `SELECT r.account_id::text,r.test_id::text FROM owned_exam_rewards r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.state='pending' AND NOT a.disabled ORDER BY r.created_at,r.account_id,r.test_id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	type item struct{ actor, test string }
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.actor, &v.test); e != nil {
			rows.Close()
			return 0, e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	var failures error
	for _, v := range items {
		ok, e := r.retryReward(ctx, v.actor, v.test)
		if e != nil {
			failures = errors.Join(failures, e)
		}
		if ok {
			count++
		}
		if ctx.Err() != nil {
			return count, errors.Join(failures, ctx.Err())
		}
	}
	return count, failures
}
func (r *Repository) retryReward(ctx context.Context, actor, test string) (bool, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var active bool
	e = tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=CURRENT_TIMESTAMP)AND(p.expires_at IS NULL OR p.expires_at>CURRENT_TIMESTAMP)) FROM owned_authority_accounts a WHERE a.id=$1 FOR UPDATE OF a`, actor).Scan(&active)
	if e != nil {
		return false, e
	}
	if !active {
		return false, tx.Commit()
	}
	var state, attempt string
	var amount int64
	e = tx.QueryRowContext(ctx, `SELECT state,attempt_id::text,amount FROM owned_exam_rewards WHERE account_id=$1 AND test_id=$2 FOR UPDATE`, actor, test).Scan(&state, &attempt, &amount)
	if errors.Is(e, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if e != nil {
		return false, e
	}
	if state != "pending" {
		return false, tx.Commit()
	}
	if e = r.cfg.Rewards.GrantExamRewardInTransaction(ctx, tx, actor, test, attempt, amount); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_exam_rewards SET state='applied' WHERE account_id=$1 AND test_id=$2`, actor, test); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return true, nil
}
