// SPDX-License-Identifier: AGPL-3.0-only
package postawardpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/sphere/postawards"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

type purchase struct {
	ID, Order, Post, Publisher, Actor, Treasury, Payee, Beneficiary, Owner, Realm, Policy string
	Amount, Share                                                                         wallet.Amount
	Attitude                                                                              int
	Fulfilled                                                                             *time.Time
}

func load(ctx context.Context, tx *sql.Tx, id string, locked bool) (p purchase, e error) {
	suffix := ""
	if locked {
		suffix = " FOR UPDATE NOWAIT"
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,order_id::text,post_id::text,publisher_id::text,account_id::text,treasury_account_id::text,treasury_wallet_id::text,beneficiary_wallet_id::text,COALESCE(beneficiary_account_id::text,''),COALESCE(beneficiary_realm_id::text,''),policy,amount::text,share_amount::text,attitude,fulfilled_at FROM owned_post_award_purchases WHERE id=$1::uuid`+suffix, id).Scan(&p.ID, &p.Order, &p.Post, &p.Publisher, &p.Actor, &p.Treasury, &p.Payee, &p.Beneficiary, &p.Owner, &p.Realm, &p.Policy, &p.Amount, &p.Share, &p.Attitude, &p.Fulfilled)
	return p, mapped(e)
}
func (r *Repository) payment(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order) (p purchase, e error) {
	if r == nil || r.db != db || db == nil || tx == nil || o.AppIdentifier != "internal" || o.ProductIdentifier == nil || *o.ProductIdentifier != a.Product {
		return p, a.ErrUnavailable
	}
	if _, e = r.guard.Preflight(ctx, db, tx, actor, "orders.pay", "posts.award"); e != nil {
		return
	}
	var id string
	if json.Unmarshal(o.Meta["purchase_id"], &id) != nil || !authority.ValidID(id) {
		return p, a.ErrInvalid
	}
	p, e = load(ctx, tx, id, false)
	if e != nil {
		return
	}
	if p.Actor != actor || p.Order != o.ID || p.Amount.Cmp(o.Amount) != 0 || o.Currency != a.Currency || p.Payee != o.PayeeWalletID || p.Payee != r.cfg.TreasuryWalletID || p.Treasury != r.cfg.TreasuryAccountID {
		return p, a.ErrConflict
	}
	s, e := r.lockScope(ctx, tx, actor, "", p.Post, "posts.award", "orders.pay")
	if e != nil {
		return p, e
	}
	if s.Publisher != p.Publisher || s.Owner != p.Owner || s.Realm != p.Realm {
		return p, a.ErrConflict
	}
	beneficiary, e := r.binding(ctx, tx, s)
	if e != nil {
		return p, e
	}
	if beneficiary != p.Beneficiary {
		return p, a.ErrConflict
	}
	p, e = load(ctx, tx, id, true)
	if e != nil {
		return
	}
	if p.Fulfilled != nil {
		return p, a.ErrConflict
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.orders WHERE id=$1::uuid AND actor_id=$2::uuid AND app_id IS NULL AND subscription_id IS NULL AND gift_id IS NULL AND product_identifier='posts.award' AND currency='points' AND amount=$3::numeric AND payee_wallet_id=$4::uuid AND status=0)`, o.ID, actor, p.Amount, p.Payee).Scan(&valid)
	if e != nil {
		return
	}
	if !valid {
		return p, a.ErrForbidden
	}
	return
}
func (r *Repository) PreparePostAward(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order, payer string, at time.Time) (e error) {
	defer func() { e = mapped(e) }()
	p, e := r.payment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if payer == p.Payee {
		return a.ErrInvalid
	}
	if e = walletpg.LockAwardWallets(ctx, tx, payer, p.Payee, p.Beneficiary); e != nil {
		return e
	}
	var yes bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE id=$1::uuid AND account_id=$2::uuid AND realm_id IS NULL AND deleted_at IS NULL)AND EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE id=$3::uuid AND account_id=$4::uuid AND realm_id IS NULL AND deleted_at IS NULL)`, payer, actor, p.Payee, p.Treasury).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes {
		return a.ErrForbidden
	}
	return r.walletMatches(ctx, tx, p.Beneficiary, scope{Owner: p.Owner, Realm: p.Realm})
}
func (r *Repository) FulfillPostAward(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order, at time.Time) (e error) {
	defer func() { e = mapped(e) }()
	p, e := r.payment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if o.TransactionID == nil || o.PayerWalletID == nil {
		return a.ErrForbidden
	}
	var yes bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.transactions WHERE id=$1::uuid AND managed_by=$2::uuid AND purpose='commerce' AND actor_id=$3::uuid AND amount=$4::numeric AND currency='points' AND payer_wallet_id=$5::uuid AND payee_wallet_id=$6::uuid AND status=2 AND confirmed_at IS NOT NULL AND deleted_at IS NULL)`, *o.TransactionID, o.ID, actor, p.Amount, *o.PayerWalletID, p.Payee).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes {
		return a.ErrForbidden
	}
	at = r.cfg.Now().UTC().Truncate(time.Microsecond)
	var distribution, reserve *string
	var settled *time.Time
	if p.Share.Sign() > 0 {
		d, x := r.commerce.ReservePostAwardShare(ctx, db, tx, actor, p.ID, p.Treasury, p.Beneficiary, o, p.Share, at)
		if x != nil {
			return x
		}
		distribution = &d.ID
		reserve = d.TransactionID
	} else {
		settled = &at
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_award_receipts(purchase_id,transaction_id,distribution_order_id,reserve_transaction_id,due_at,settled_at,created_at,next_attempt_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::timestamptz,$6::timestamptz,$5::timestamptz,$5::timestamptz)`, p.ID, *o.TransactionID, distribution, reserve, at, settled)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_post_award_purchases SET fulfilled_at=$2::timestamptz WHERE id=$1::uuid AND fulfilled_at IS NULL`, p.ID, at); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_text_posts SET awarded_score=awarded_score+CASE WHEN $3::integer=0 THEN $2::numeric ELSE -$2::numeric END WHERE id=$1::uuid`, p.Post, p.Amount, p.Attitude); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, p.Publisher, p.Post, p.Order, "fulfilled", at); e != nil {
		return e
	}
	_, e = r.guard.Authorize(ctx, db, tx, actor, "orders.pay", "posts.award")
	return e
}
