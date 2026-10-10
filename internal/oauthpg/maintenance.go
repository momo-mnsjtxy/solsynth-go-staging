// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"context"
	"solsynth.local/go-migration/ports/authority/oauth"
)

// ProcessDue retires at most limit expired roots, preserving a 24-hour replay
// evidence window. Canonical sessions and audit records are not deleted. Active
// token families retain their complete (bounded) refresh history.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, oauth.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, oauth.ErrRequest
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	result, e := tx.ExecContext(ctx, `WITH due AS(SELECT r.id FROM owned_oauth_requests r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.expires_at<clock_timestamp()-interval '24 hours' ORDER BY r.expires_at,r.id LIMIT $1 FOR UPDATE OF a SKIP LOCKED)DELETE FROM owned_oauth_requests r USING due WHERE r.id=due.id`, limit)
	if e != nil {
		return 0, e
	}
	count, e := result.RowsAffected()
	if e != nil {
		return 0, e
	}
	if remain := limit - int(count); remain > 0 {
		result, e = tx.ExecContext(ctx, `WITH due AS(SELECT g.session_id FROM owned_oauth_grants g JOIN owned_authority_accounts a ON a.id=g.account_id WHERE g.expires_at<clock_timestamp()-interval '24 hours' OR g.revoked_at<clock_timestamp()-interval '24 hours' ORDER BY g.expires_at,g.session_id LIMIT $1 FOR UPDATE OF a SKIP LOCKED)DELETE FROM owned_oauth_grants g USING due WHERE g.session_id=due.session_id`, remain)
		if e != nil {
			return 0, e
		}
		n, x := result.RowsAffected()
		if x != nil {
			return 0, x
		}
		count += n
	}
	if remain := limit - int(count); remain > 0 {
		result, e = tx.ExecContext(ctx, `WITH due AS(SELECT hash FROM owned_oauth_devices WHERE expires_at<clock_timestamp()-interval '24 hours' ORDER BY expires_at,hash LIMIT $1 FOR UPDATE SKIP LOCKED)DELETE FROM owned_oauth_devices d USING due WHERE d.hash=due.hash`, remain)
		if e != nil {
			return 0, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return 0, e
		}
		count += n
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	return int(count), nil
}
