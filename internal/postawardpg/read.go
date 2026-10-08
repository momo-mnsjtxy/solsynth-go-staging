// SPDX-License-Identifier: AGPL-3.0-only
package postawardpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/sphere/postawards"
)

func (r *Repository) read(ctx context.Context, actor string) (*sql.Tx, error) {
	if r == nil {
		return nil, a.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return nil, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		var ok bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&ok)
		if e != nil || !ok {
			tx.Rollback()
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			return nil, authority.ErrInvalid
		}
	}
	return tx, nil
}
func (r *Repository) List(ctx context.Context, actor, post string, q a.Page) (out []a.Award, total int, e error) {
	out = []a.Award{}
	q, e = q.Checked()
	if e != nil {
		return
	}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if _, e = r.visiblePost(ctx, tx, actor, post); e != nil {
		return
	}
	where := ` FROM owned_post_award_purchases p JOIN owned_post_award_receipts r ON r.purchase_id=p.id WHERE p.post_id=$1::uuid AND p.fulfilled_at IS NOT NULL AND EXISTS(SELECT 1 FROM owned_authority_accounts u WHERE u.id=p.account_id AND NOT u.disabled)`
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, post).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text,p.post_id::text,p.account_id::text,p.amount::text,p.attitude,p.message,p.fulfilled_at,r.settled_at`+where+` ORDER BY p.fulfilled_at DESC,p.id LIMIT $2::integer OFFSET $3::integer`, post, q.Take, q.Offset)
	if e != nil {
		return
	}
	for rows.Next() {
		var v a.Award
		if e = rows.Scan(&v.ID, &v.PostID, &v.AccountID, &v.Amount, &v.Attitude, &v.Message, &v.CreatedAt, &v.SettledAt); e != nil {
			rows.Close()
			return
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	return out, total, tx.Commit()
}
func (r *Repository) Pending(ctx context.Context, actor, post string) (out a.Pending, e error) {
	tx, e := r.read(ctx, actor)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if _, e = r.visiblePost(ctx, tx, actor, post); e != nil {
		return
	}
	e = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(p.amount),0)::text,COALESCE(sum(p.share_amount),0)::text FROM owned_post_award_purchases p JOIN owned_post_award_receipts r ON r.purchase_id=p.id WHERE p.post_id=$1::uuid AND p.attitude=0 AND p.fulfilled_at IS NOT NULL AND r.settled_at IS NULL`, post).Scan(&out.Count, &out.TotalAmount, &out.PayoutAmount)
	if e != nil {
		return
	}
	return out, tx.Commit()
}
func (r *Repository) Settlements(ctx context.Context, actor, selector string, q a.Page) (out []a.Settlement, total int, e error) {
	out = []a.Settlement{}
	q, e = q.Checked()
	if e != nil {
		return
	}
	tx, s, e := r.merchantTx(ctx, actor, selector, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_post_award_purchases p JOIN owned_post_award_receipts r ON r.purchase_id=p.id WHERE p.publisher_id=$1::uuid AND p.attitude=0`, s.Publisher).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text,p.post_id::text,p.order_id::text,p.beneficiary_wallet_id::text,'points',p.share_amount::text,p.policy,r.due_at,r.settled_at FROM owned_post_award_purchases p JOIN owned_post_award_receipts r ON r.purchase_id=p.id WHERE p.publisher_id=$1::uuid AND p.attitude=0 ORDER BY r.created_at DESC,p.id LIMIT $2::integer OFFSET $3::integer`, s.Publisher, q.Take, q.Offset)
	if e != nil {
		return
	}
	for rows.Next() {
		var v a.Settlement
		if e = rows.Scan(&v.AwardID, &v.PostID, &v.OrderID, &v.PaymentWalletID, &v.Currency, &v.Amount, &v.Policy, &v.DueAt, &v.SettledAt); e != nil {
			rows.Close()
			return
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	return out, total, r.commit(ctx, tx, actor)
}
