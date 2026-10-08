// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/namecards"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

// NameCardFulfillment is a trusted same-pool transaction participant. PayOrder
// invokes it only for the fixed internal product, after canonical settlement.
// Implementations must verify the stored purchase/order/wallet relation.
type NameCardFulfillment interface {
	FulfillNameCard(context.Context, *sql.DB, *sql.Tx, string, c.Order, time.Time) error
}

func (r *Repository) WithNameCards(p NameCardFulfillment) *Repository { r.namecards = p; return r }

func (r *Repository) nameCardBoundary(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) error {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil || !authority.ValidID(actor) {
		return c.ErrUnavailable
	}
	_, e := r.guard.Authenticate(ctx, db, tx, actor)
	return e
}

// NameCardRequest reuses canonical commerce idempotency, scoped to this fixed
// product and operation. Empty resource reads/locks; a resource records success.
func (r *Repository) NameCardRequest(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, key, operation, fp, resource string) (string, error) {
	if e := r.nameCardBoundary(ctx, db, tx, actor); e != nil {
		return "", e
	}
	if !namecards.ValidKey(key) || len(fp) != 64 || (operation != "order" && operation != "use") {
		return "", c.ErrInvalid
	}
	owner := "namecard:" + actor
	kind := "namecard." + operation
	if resource == "" {
		return idem(ctx, tx, owner, key, kind, fp)
	}
	if !authority.ValidID(resource) {
		return "", c.ErrInvalid
	}
	return resource, recordIdem(ctx, tx, owner, key, kind, fp, resource)
}

// CreateNameCardOrder is not exposed by generic merchant order APIs. Price and
// points/payee mapping come from explicit operator configuration, never a body.
func (r *Repository) CreateNameCardOrder(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purchase, currency, payee string, amount wallet.Amount, now time.Time) (c.Order, error) {
	var out c.Order
	if e := r.nameCardBoundary(ctx, db, tx, actor); e != nil {
		return out, e
	}
	if !authority.ValidID(purchase) || !authority.ValidID(payee) || amount.Sign() <= 0 || currency == "" || len(currency) > 128 {
		return out, c.ErrInvalid
	}
	var live bool
	if e := tx.QueryRowContext(ctx, `SELECT true FROM wallet_v1.wallets WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE`, payee).Scan(&live); e != nil {
		return out, notFound(e)
	}
	oid, e := id()
	if e != nil {
		return out, e
	}
	product, remarks := namecards.Product, "Purchase name change card"
	accountJSON, _ := json.Marshal(actor)
	purchaseJSON, _ := json.Marshal(purchase)
	now = now.UTC().Truncate(time.Microsecond)
	out = c.Order{ID: oid, Status: c.Unpaid, Currency: currency, Amount: amount, AppIdentifier: "internal", ProductIdentifier: &product, Remarks: &remarks, Meta: map[string]json.RawMessage{"account_id": accountJSON, "purchase_id": purchaseJSON}, PayeeWalletID: payee, Items: []c.OrderItem{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(24 * time.Hour)}
	if e = insertOrder(ctx, tx, out, "", actor, "", ""); e != nil {
		return c.Order{}, e
	}
	return out, nil
}
