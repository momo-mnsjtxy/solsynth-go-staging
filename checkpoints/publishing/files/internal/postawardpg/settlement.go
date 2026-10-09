// SPDX-License-Identifier: AGPL-3.0-only
package postawardpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/sphere/postawards"
	"time"
)

// Settlement uses only captured beneficiary ancestry, not the publisher's
// current wallet setting. Private or deleted content never cancels an owed hold.
func (r *Repository) release(ctx context.Context, tx *sql.Tx, id string) error {
	p, e := load(ctx, tx, id, false)
	if e != nil {
		return e
	}
	if p.Payee != r.cfg.TreasuryWalletID || p.Treasury != r.cfg.TreasuryAccountID {
		return a.ErrUnavailable
	}
	var realmOwner string
	if p.Realm != "" {
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL`, p.Realm).Scan(&realmOwner); e != nil {
			return mapped(e)
		}
	}
	for _, account := range unique(p.Treasury, p.Owner, realmOwner) {
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE NOWAIT`, account).Scan(&active); e != nil {
			return e
		}
		if !active {
			return a.ErrUnavailable
		}
	}
	if p.Realm != "" {
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, p.Realm).Scan(&owner); e != nil {
			return e
		}
		if owner != realmOwner {
			return a.ErrRetry
		}
	}
	p, e = load(ctx, tx, id, true)
	if e != nil {
		return e
	}
	var settled *time.Time
	var distribution *string
	if e = tx.QueryRowContext(ctx, `SELECT settled_at,distribution_order_id::text FROM owned_post_award_receipts WHERE purchase_id=$1::uuid FOR UPDATE NOWAIT`, id).Scan(&settled, &distribution); e != nil {
		return e
	}
	if settled != nil {
		return nil
	}
	if distribution == nil {
		return a.ErrConflict
	}
	if e = walletpg.LockAwardWallets(ctx, tx, p.Payee, p.Beneficiary); e != nil {
		return e
	}
	if e = r.walletMatches(ctx, tx, p.Beneficiary, scope{Owner: p.Owner, Realm: p.Realm}); e != nil {
		return e
	}
	var treasury bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE id=$1::uuid AND account_id=$2::uuid AND realm_id IS NULL AND deleted_at IS NULL)`, p.Payee, p.Treasury).Scan(&treasury); e != nil {
		return e
	}
	if !treasury {
		return a.ErrUnavailable
	}
	at := r.cfg.Now().UTC().Truncate(time.Microsecond)
	if e = r.commerce.ReleasePostAwardShare(ctx, r.db, tx, id, at); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_post_award_receipts SET settled_at=$2::timestamptz WHERE purchase_id=$1::uuid AND settled_at IS NULL`, id, at); e != nil {
		return e
	}
	return audit(ctx, tx, "", p.Publisher, p.Post, p.Order, "settled", at)
}
func (r *Repository) due(ctx context.Context, tx *sql.Tx, publisher string, limit int) ([]string, error) {
	rows, e := tx.QueryContext(ctx, `SELECT r.purchase_id::text FROM owned_post_award_receipts r JOIN owned_post_award_purchases p ON p.id=r.purchase_id WHERE r.settled_at IS NULL AND r.distribution_order_id IS NOT NULL AND r.due_at<=$1::timestamptz AND ($2::text='' OR p.publisher_id=NULLIF($2::text,'')::uuid) AND p.treasury_account_id=$3::uuid AND p.treasury_wallet_id=$4::uuid AND($2::text<>'' OR r.next_attempt_at<=$1::timestamptz) ORDER BY r.next_attempt_at,r.purchase_id LIMIT $5::integer`, r.cfg.Now(), publisher, r.cfg.TreasuryAccountID, r.cfg.TreasuryWalletID, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if r == nil {
		return 0, a.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, a.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return 0, e
	}
	ids, e := r.due(ctx, tx, "", limit)
	tx.Rollback()
	if e != nil {
		return 0, e
	}
	n := 0
	var result error
	for _, id := range ids {
		tx, e = r.db.BeginTx(ctx, nil)
		if e != nil {
			return n, e
		}
		e = r.release(ctx, tx, id)
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
		if e == nil {
			n++
			continue
		}
		result = errors.Join(result, mapped(e))
		_, x := r.db.ExecContext(ctx, `UPDATE owned_post_award_receipts SET attempts=LEAST(attempts+1,30),next_attempt_at=$2::timestamptz+LEAST(3600,1<<LEAST(attempts,12))*interval '1 second' WHERE purchase_id=$1::uuid AND settled_at IS NULL`, id, r.cfg.Now())
		result = errors.Join(result, x)
	}
	return n, result
}
func (r *Repository) SettleMerchant(ctx context.Context, actor, selector string, limit int) (n int, e error) {
	defer func() { e = mapped(e) }()
	if limit < 1 || limit > 100 {
		return 0, a.ErrInvalid
	}
	tx, s, e := r.merchantTx(ctx, actor, selector, "merchants.settlements.manage")
	if e != nil {
		return
	}
	defer tx.Rollback()
	ids, e := r.due(ctx, tx, s.Publisher, limit)
	if e != nil {
		return 0, e
	}
	for _, id := range ids {
		if e = r.release(ctx, tx, id); e != nil {
			return 0, e
		}
		n++
	}
	return n, r.commit(ctx, tx, actor, "merchants.settlements.manage")
}
func (r *Repository) SettlePublisherRewards(ctx context.Context, actor string, limit int) (int, error) {
	return r.settleAll(ctx, actor, a.ResettleRequest{}, limit, "publishers.rewards.settle", false)
}
func (r *Repository) SettlePublisherAwards(ctx context.Context, actor string, limit int) (int, error) {
	return r.settleAll(ctx, actor, a.ResettleRequest{}, limit, "publishers.rewards.settle", false)
}
func (r *Repository) ResettlePublisherRewards(ctx context.Context, actor string, q a.ResettleRequest, limit int) (int, error) {
	return r.settleAll(ctx, actor, q, limit, "publishers.rewards.resettle", true)
}
func (r *Repository) settleAll(ctx context.Context, actor string, q a.ResettleRequest, limit int, permission string, reset bool) (n int, e error) {
	defer func() { e = mapped(e) }()
	if !authority.ValidID(actor) || limit < 1 || limit > 100 || reset && (q.DateFrom.IsZero() || q.DateTo.IsZero() || !q.DateFrom.Before(q.DateTo) || q.PublisherID != nil && !authority.ValidID(*q.PublisherID)) {
		return 0, a.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor, permission); e != nil {
		return 0, e
	}
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor).Scan(&active); e != nil || !active {
		if e != nil {
			return 0, e
		}
		return 0, a.ErrForbidden
	}
	if e = r.checkAuthority(ctx, tx, actor, permission); e != nil {
		return 0, e
	}
	if reset {
		publisher := ""
		if q.PublisherID != nil {
			publisher = *q.PublisherID
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_post_award_receipts r SET next_attempt_at=$6,attempts=0 FROM owned_post_award_purchases p WHERE p.id=r.purchase_id AND r.settled_at IS NULL AND p.fulfilled_at>=$1 AND p.fulfilled_at<$2 AND($3::text='' OR p.publisher_id=NULLIF($3::text,'')::uuid) AND p.treasury_account_id=$4::uuid AND p.treasury_wallet_id=$5::uuid`, q.DateFrom, q.DateTo, publisher, r.cfg.TreasuryAccountID, r.cfg.TreasuryWalletID, r.cfg.Now()); e != nil {
			return
		}
	}
	var ids []string
	if reset {
		publisher := ""
		if q.PublisherID != nil {
			publisher = *q.PublisherID
		}
		rows, x := tx.QueryContext(ctx, `SELECT r.purchase_id::text FROM owned_post_award_receipts r JOIN owned_post_award_purchases p ON p.id=r.purchase_id WHERE r.settled_at IS NULL AND r.distribution_order_id IS NOT NULL AND r.due_at<=$1 AND p.fulfilled_at>=$2 AND p.fulfilled_at<$3 AND($4::text='' OR p.publisher_id=NULLIF($4::text,'')::uuid) AND p.treasury_account_id=$5::uuid AND p.treasury_wallet_id=$6::uuid ORDER BY r.next_attempt_at,r.purchase_id LIMIT $7`, r.cfg.Now(), q.DateFrom, q.DateTo, publisher, r.cfg.TreasuryAccountID, r.cfg.TreasuryWalletID, limit)
		if x != nil {
			return 0, x
		}
		for rows.Next() {
			var id string
			if x = rows.Scan(&id); x != nil {
				rows.Close()
				return 0, x
			}
			ids = append(ids, id)
		}
		x = rows.Err()
		rows.Close()
		if x != nil {
			return 0, x
		}
	} else {
		ids, e = r.due(ctx, tx, "", limit)
	}
	if e != nil {
		return 0, e
	}
	for _, id := range ids {
		if e = r.release(ctx, tx, id); e != nil {
			return 0, e
		}
		n++
	}
	return n, r.commit(ctx, tx, actor, permission)
}
