// SPDX-License-Identifier: AGPL-3.0-only
package realmboostpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"time"
)

type purchase struct {
	ID, Order, Realm, Actor, Treasury, Payee, Currency string
	Shares                                             int64
	Amount                                             wallet.Amount
	Fulfilled                                          *time.Time
}

func load(ctx context.Context, tx *sql.Tx, id string, lock bool) (p purchase, e error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE NOWAIT"
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,order_id::text,realm_id::text,account_id::text,treasury_account_id::text,treasury_wallet_id::text,currency,shares,amount::text,fulfilled_at FROM owned_realm_boost_purchases WHERE id=$1::uuid`+suffix, id).Scan(&p.ID, &p.Order, &p.Realm, &p.Actor, &p.Treasury, &p.Payee, &p.Currency, &p.Shares, &p.Amount, &p.Fulfilled)
	return p, mapped(e)
}
func (r *Repository) payment(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order) (p purchase, e error) {
	if r == nil || r.db != db || db == nil || tx == nil || o.AppIdentifier != "internal" || o.ProductIdentifier == nil || *o.ProductIdentifier != b.Product {
		return p, b.ErrUnavailable
	}
	if _, e = r.guard.Preflight(ctx, db, tx, actor, "orders.pay", "realms.boosts.manage"); e != nil {
		return
	}
	var id string
	if json.Unmarshal(o.Meta["purchase_id"], &id) != nil || !authority.ValidID(id) {
		return p, b.ErrInvalid
	}
	p, e = load(ctx, tx, id, false)
	if e != nil {
		return
	}
	if p.Actor != actor || p.Order != o.ID || p.Amount.Cmp(o.Amount) != 0 || p.Currency != o.Currency || p.Payee != o.PayeeWalletID || p.Payee != r.cfg.TreasuryWalletID || p.Treasury != r.cfg.TreasuryAccountID {
		return p, b.ErrConflict
	}
	expected, e := b.Price(p.Shares, p.Currency)
	if e != nil || expected.Cmp(p.Amount) != 0 {
		return p, b.ErrConflict
	}
	if _, e = r.lockRealm(ctx, tx, actor, p.Realm, true); e != nil {
		return
	}
	p, e = load(ctx, tx, id, true)
	if e != nil {
		return
	}
	if p.Fulfilled != nil {
		return p, b.ErrConflict
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.orders WHERE id=$1::uuid AND actor_id=$2::uuid AND app_id IS NULL AND subscription_id IS NULL AND gift_id IS NULL AND product_identifier=$3::text AND currency=$4::text AND amount=$5::numeric AND payee_wallet_id=$6::uuid AND status=0)`, o.ID, actor, b.Product, p.Currency, p.Amount, p.Payee).Scan(&valid)
	if e != nil {
		return
	}
	if !valid {
		return p, b.ErrForbidden
	}
	return
}
func (r *Repository) PrepareRealmBoost(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order, payer string, at time.Time) (e error) {
	defer func() { e = mapped(e) }()
	p, e := r.payment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if payer == p.Payee {
		return b.ErrInvalid
	}
	// Resolve and lock current internal subscription authority before wallet locks.
	if _, e = r.commerce.RealmBoostPerkLevel(ctx, db, tx, actor, r.cfg.Policy.PerkLevels, r.cfg.Now().UTC()); e != nil {
		return e
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
			return b.ErrForbidden
		}
	}
	return nil
}
func (r *Repository) FulfillRealmBoost(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o c.Order, at time.Time) (e error) {
	defer func() { e = mapped(e) }()
	p, e := r.payment(ctx, db, tx, actor, o)
	if e != nil {
		return e
	}
	if o.TransactionID == nil || o.PayerWalletID == nil {
		return b.ErrForbidden
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.transactions t WHERE t.id=$1::uuid AND t.managed_by=$2::uuid AND t.purpose='commerce' AND t.actor_id=$3::uuid AND t.amount=$4::numeric AND t.currency=$5::text AND t.payee_wallet_id=$6::uuid AND t.payer_wallet_id=$7::uuid AND t.status=2 AND t.confirmed_at IS NOT NULL AND t.deleted_at IS NULL)`, *o.TransactionID, o.ID, actor, p.Amount, p.Currency, p.Payee, *o.PayerWalletID).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return b.ErrForbidden
	}
	at = r.cfg.Now().UTC().Truncate(time.Microsecond)
	perk, e := r.commerce.RealmBoostPerkLevel(ctx, db, tx, actor, r.cfg.Policy.PerkLevels, at)
	if e != nil {
		return e
	}
	xp, e := b.BoostXP(p.Shares, perk)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_realm_boost_purchases SET transaction_id=$2::uuid,fulfilled_at=$3::timestamptz,expires_at=$4::timestamptz WHERE id=$1::uuid AND fulfilled_at IS NULL`, p.ID, *o.TransactionID, at, at.Add(b.ExpirationDays*24*time.Hour))
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_boost_experience(id,realm_id,account_id,order_id,delta,perk_level,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::bigint,$6::integer,$7::timestamptz)`, p.ID, p.Realm, actor, o.ID, xp, perk, at)
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, p.Realm, o.ID, "fulfilled", at); e != nil {
		return e
	}
	_, e = r.guard.Authorize(ctx, db, tx, actor, "orders.pay", "realms.boosts.manage")
	return e
}
