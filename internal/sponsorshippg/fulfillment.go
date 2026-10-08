// SPDX-License-Identifier: AGPL-3.0-only
package sponsorshippg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"time"
)

type purchase struct {
	ID, Order, Post, Actor, Publisher, Treasury, Payee, Currency string
	Amount                                                       wallet.Amount
	Fulfilled                                                    *time.Time
}

func load(ctx context.Context, tx *sql.Tx, id string, locked bool) (p purchase, e error) {
	suffix := ""
	if locked {
		suffix = " FOR UPDATE NOWAIT"
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,order_id::text,post_id::text,account_id::text,publisher_id::text,treasury_account_id::text,treasury_wallet_id::text,currency,amount::text,fulfilled_at FROM owned_post_sponsorships WHERE id=$1::uuid`+suffix, id).Scan(&p.ID, &p.Order, &p.Post, &p.Actor, &p.Publisher, &p.Treasury, &p.Payee, &p.Currency, &p.Amount, &p.Fulfilled)
	return p, mapped(e)
}
func (r *Repository) payment(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order) (p purchase, e error) {
	if r == nil || r.db != db || db == nil || tx == nil || o.AppIdentifier != "internal" || o.ProductIdentifier == nil || *o.ProductIdentifier != s.Product {
		return p, s.ErrUnavailable
	}
	if _, e = r.guard.Preflight(ctx, db, tx, actor, "orders.pay", "posts.sponsor"); e != nil {
		return
	}
	var id string
	if json.Unmarshal(o.Meta["purchase_id"], &id) != nil || !authority.ValidID(id) {
		return p, s.ErrInvalid
	}
	p, e = load(ctx, tx, id, false)
	if e != nil {
		return
	}
	if p.Actor != actor || p.Order != o.ID || p.Amount.Cmp(o.Amount) != 0 || p.Currency != o.Currency || p.Currency != "golds" || p.Payee != o.PayeeWalletID || p.Payee != r.cfg.TreasuryWalletID || p.Treasury != r.cfg.TreasuryAccountID {
		return p, s.ErrConflict
	}
	post, e := r.lockPost(ctx, tx, actor, p.Post, true)
	if e != nil {
		return p, e
	}
	if post.PublisherID != p.Publisher {
		return p, s.ErrConflict
	}
	p, e = load(ctx, tx, id, true)
	if e != nil {
		return
	}
	if p.Fulfilled != nil {
		return p, s.ErrConflict
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.orders WHERE id=$1::uuid AND actor_id=$2::uuid AND app_id IS NULL AND subscription_id IS NULL AND gift_id IS NULL AND product_identifier=$3::text AND currency='golds' AND amount=$4::numeric AND payee_wallet_id=$5::uuid AND status=0)`, o.ID, actor, s.Product, p.Amount, p.Payee).Scan(&valid)
	if e != nil {
		return
	}
	if !valid {
		return p, s.ErrForbidden
	}
	return
}
func (r *Repository) PrepareSponsor(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order, payer string, at time.Time) (e error) {
	defer func() { e = mapped(e) }()
	p, e := r.payment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if payer == p.Payee {
		return s.ErrInvalid
	}
	ids := []string{payer, p.Payee}
	sort.Strings(ids)
	for _, id := range ids {
		var owner string
		e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND realm_id IS NULL AND deleted_at IS NULL FOR UPDATE NOWAIT`, id).Scan(&owner)
		if e != nil {
			return e
		}
		expected := actor
		if id == p.Payee {
			expected = p.Treasury
		}
		if owner != expected {
			return s.ErrForbidden
		}
	}
	return nil
}
func (r *Repository) FulfillSponsor(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order, at time.Time) (e error) {
	defer func() { e = mapped(e) }()
	p, e := r.payment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if o.TransactionID == nil || o.PayerWalletID == nil {
		return s.ErrForbidden
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.transactions t WHERE t.id=$1::uuid AND t.managed_by=$2::uuid AND t.purpose='commerce' AND t.actor_id=$3::uuid AND t.amount=$4::numeric AND t.currency='golds' AND t.payee_wallet_id=$5::uuid AND t.payer_wallet_id=$6::uuid AND t.status=2 AND t.confirmed_at IS NOT NULL AND t.deleted_at IS NULL)`, *o.TransactionID, o.ID, actor, p.Amount, p.Payee, *o.PayerWalletID).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return s.ErrForbidden
	}
	at = r.cfg.Now().UTC().Truncate(time.Microsecond)
	_, e = tx.ExecContext(ctx, `UPDATE owned_post_sponsorships SET transaction_id=$2::uuid,fulfilled_at=$3::timestamptz,expires_at=$4::timestamptz WHERE id=$1::uuid AND fulfilled_at IS NULL`, p.ID, *o.TransactionID, at, at.Add(24*time.Hour))
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, p.Post, o.ID, "fulfilled", at); e != nil {
		return e
	}
	_, e = r.guard.Authorize(ctx, db, tx, actor, "orders.pay", "posts.sponsor")
	return e
}
