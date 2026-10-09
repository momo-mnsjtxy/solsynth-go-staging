// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/develop/apps"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"strings"
)

// Coupon checkouts opt into current catalog terms. Product locks precede the
// order and coupon locks, matching catalog writers. Ordinary orders keep their
// existing captured-price policy and never call this helper.
func lockCouponOrderProducts(ctx context.Context, tx *sql.Tx, o c.Order) error {
	app := strings.TrimPrefix(o.AppIdentifier, "developer.app:")
	if o.AppIdentifier != "developer.app:"+app || !wallet.ValidID(app) || len(o.Items) == 0 {
		return c.ErrUnavailable
	}
	products, e := couponProducts(ctx, tx, app, orderProducts(o))
	if e != nil {
		return e
	}
	for _, p := range products {
		var item *c.OrderItem
		for i := range o.Items {
			if o.Items[i].ProductIdentifier == p.Identifier {
				item = &o.Items[i]
				break
			}
		}
		if item == nil || item.UnitPrice != p.Price || item.Currency != p.Currency || p.Currency != o.Currency {
			return c.ErrConflict
		}
		var reserved bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.order_items WHERE order_id=$1::uuid AND product_id=$2::uuid AND identifier=$3 AND quantity=$4)`, o.ID, p.ID, p.Identifier, item.Quantity).Scan(&reserved)
		if e != nil {
			return e
		}
		if !reserved {
			return c.ErrConflict
		}
	}
	return nil
}

func couponProducts(ctx context.Context, tx *sql.Tx, app string, names []string) ([]apps.Product, error) {
	if len(names) == 0 {
		return nil, c.ErrConflict
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	products := make([]apps.Product, 0, len(names))
	for _, name := range names {
		var raw []byte
		var id string
		e := tx.QueryRowContext(ctx, `SELECT id::text,document FROM owned_app_products WHERE app_id=$1::uuid AND identifier=$2 AND deleted_at IS NULL FOR SHARE`, app, name).Scan(&id, &raw)
		if e != nil {
			return nil, notFound(e)
		}
		var p apps.Product
		if e = json.Unmarshal(raw, &p); e != nil {
			return nil, e
		}
		if p.ID != id || p.AppID != app || p.Identifier != name || !p.State.IsEnabled {
			return nil, c.ErrConflict
		}
		products = append(products, p)
	}
	return products, nil
}

// Capture the complete canonical catalog snapshot, including its version and
// policies. Hash comparison rejects changes; it never recalculates a saved net.
func couponCheckoutTerms(ctx context.Context, tx *sql.Tx, app, payee, scope string, products []string) (string, error) {
	if scope == "order" {
		current, e := couponProducts(ctx, tx, app, products)
		if e != nil {
			return "", e
		}
		return fingerprint(current)
	}
	if (scope != "subscription" && scope != "gift") || len(products) != 1 {
		return "", c.ErrConflict
	}
	d, e := definition(ctx, tx, products[0])
	if e != nil {
		return "", e
	}
	if d.AppIdentifier == nil || *d.AppIdentifier != "developer.app:"+app || d.PayeeWalletID != payee {
		return "", c.ErrConflict
	}
	var product *apps.Product
	if d.ProductID != "" {
		current, err := couponProducts(ctx, tx, app, []string{d.ProductIdentifier})
		if err != nil {
			return "", err
		}
		if current[0].ID != d.ProductID {
			return "", c.ErrConflict
		}
		product = &current[0]
	}
	return fingerprint(struct {
		Definition   c.Definition
		Payee        string
		ProductTerms string
		Product      *apps.Product
	}{d, d.PayeeWalletID, d.ProductTerms, product})
}
