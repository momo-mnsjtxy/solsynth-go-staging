// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type awardAccounts interface {
	live.ChatAccountProvider
	PublicAccountInTransaction(context.Context, *sql.Tx, string) (accounts.Account, error)
}

var _ live.AwardRepository = (*Repository)(nil)

func (r *Repository) awardTotals(ctx context.Context, tx *sql.Tx, v *live.Stream) error {
	if r.awards == nil {
		return nil
	}
	return tx.QueryRowContext(ctx, `SELECT COALESCE(sum(p.amount)FILTER(WHERE r.payout_state IN('held','released')),0)::text,COALESCE(sum(p.amount)FILTER(WHERE r.payout_state='released'),0)::text FROM owned_livestream_award_purchases p JOIN owned_livestream_award_receipts r ON r.purchase_id=p.id WHERE p.stream_id=$1::uuid AND p.fulfilled_at IS NOT NULL`, v.ID).Scan(&v.TotalAwardScore, &v.DistributedAwardAmount)
}
func (r *Repository) ListAwards(ctx context.Context, actor, sid string, limit, offset int, active bool) (out []live.Award, total int, e error) {
	if e = r.awardsConfigured(); e != nil {
		return
	}
	if !authority.ValidID(sid) || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, 0, live.ErrInvalid
	}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	v, e := get(ctx, tx, sid, false, false)
	if e != nil {
		return nil, 0, e
	}
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return nil, 0, e
	}
	predicate := `stream_id=$1::uuid AND fulfilled_at IS NOT NULL AND EXISTS(SELECT 1 FROM owned_livestream_award_receipts receipt WHERE receipt.purchase_id=owned_livestream_award_purchases.id AND receipt.payout_state IN('held','released'))`
	order := `fulfilled_at DESC,id`
	if active {
		predicate += ` AND fulfilled_at+trunc(amount*2)*interval '1 second'>clock_timestamp()`
		order = `amount DESC,fulfilled_at DESC,id`
	}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_livestream_award_purchases WHERE `+predicate, sid).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,amount::text,message,stream_id::text,account_id::text,sender_name,fulfilled_at FROM owned_livestream_award_purchases WHERE `+predicate+` ORDER BY `+order+` LIMIT $2::integer OFFSET $3::integer`, sid, limit, offset)
	if e != nil {
		return nil, 0, e
	}
	out = []live.Award{}
	for rows.Next() {
		var a live.Award
		if e = rows.Scan(&a.ID, &a.Amount, &a.Message, &a.LiveStreamID, &a.AccountID, &a.SenderName, &a.CreatedAt); e != nil {
			rows.Close()
			return nil, 0, e
		}
		a.Attitude = 1
		a.UpdatedAt = a.CreatedAt
		out = append(out, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}
func (r *Repository) AwardLeaderboard(ctx context.Context, actor, sid string, limit int) (out []live.AwardLeader, e error) {
	if e = r.awardsConfigured(); e != nil {
		return
	}
	if !authority.ValidID(sid) || limit < 1 || limit > 100 {
		return nil, live.ErrInvalid
	}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	v, e := get(ctx, tx, sid, false, false)
	if e != nil {
		return nil, e
	}
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT account_id::text,sum(amount)::text,count(*) FROM owned_livestream_award_purchases p WHERE stream_id=$1::uuid AND fulfilled_at IS NOT NULL AND EXISTS(SELECT 1 FROM owned_livestream_award_receipts receipt WHERE receipt.purchase_id=p.id AND receipt.payout_state IN('held','released')) GROUP BY account_id ORDER BY sum(amount) DESC,account_id LIMIT $2::integer`, sid, limit)
	if e != nil {
		return nil, e
	}
	out = []live.AwardLeader{}
	for rows.Next() {
		var a live.AwardLeader
		if e = rows.Scan(&a.AccountID, &a.TotalAmount, &a.AwardCount); e != nil {
			rows.Close()
			return nil, e
		}
		a.Rank = len(out) + 1
		out = append(out, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	provider, ok := r.publishers.(awardAccounts)
	if !ok {
		return nil, live.ErrUnavailable
	}
	for i := range out {
		a, x := provider.PublicAccountInTransaction(ctx, tx, out[i].AccountID)
		if errors.Is(x, accounts.ErrNotFound) {
			continue
		}
		if x != nil {
			return nil, x
		}
		out[i].Account = &a
	}
	return out, tx.Commit()
}
