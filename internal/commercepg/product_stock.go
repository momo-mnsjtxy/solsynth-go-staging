// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

// StockWindow follows the retained authored OrderController's UTC windows.
// A nil manual boundary preserves historical products without a restock stamp.
func stockWindow(mode string, restocked *time.Time, now time.Time) (*time.Time, error) {
	now = now.UTC()
	var start time.Time
	switch mode {
	case "unlimited":
		return nil, nil
	case "manual":
		if restocked == nil {
			return nil, nil
		}
		start = restocked.UTC().Truncate(time.Microsecond)
	case "daily":
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case "weekly":
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	case "monthly":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "yearly":
		start = time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	default:
		return nil, c.ErrUnavailable
	}
	return &start, nil
}

// Reservation state is derived from the immutable order item and canonical
// order status, not a counter: unpaid live orders reserve; paid/finished consume;
// cancellation and expiry release. The caller owns the product FOR UPDATE lock
// until the order and its item are committed, serializing concurrent admission.
func checkProductStock(ctx context.Context, tx *sql.Tx, p Product, quantity int, now time.Time) error {
	if !p.Enabled || quantity < 1 || quantity > 1000 {
		return c.ErrInvalid
	}
	start, e := stockWindow(p.StockMode, p.LastRestockedAt, now)
	if e != nil {
		return e
	}
	if p.StockMode == "unlimited" {
		return nil
	}
	if start != nil && start.After(now) {
		return c.ErrConflict
	}
	if p.StockQuantity == nil || *p.StockQuantity < 0 {
		return c.ErrUnavailable
	}
	var used int64
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(i.quantity),0) FROM commerce_v1.order_items i JOIN commerce_v1.orders o ON o.id=i.order_id WHERE i.product_id=$1::uuid AND ($3::timestamptz IS NULL OR o.created_at>=$3::timestamptz) AND (o.status IN(1,3) OR o.status=0 AND o.expires_at>$2::timestamptz)`, p.ID, now, start).Scan(&used)
	if e != nil {
		return e
	}
	if used > *p.StockQuantity-int64(quantity) {
		return c.ErrStock
	}
	return nil
}

func reserveProduct(ctx context.Context, tx *sql.Tx, orderID, productID, identifier string, quantity int) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO commerce_v1.order_items(order_id,product_id,identifier,quantity) VALUES($1::uuid,$2::uuid,$3,$4)`, orderID, productID, identifier, quantity)
	return e
}

// definitionForOrder has already acquired the product write lock. Each paid
// base cycle consumes one unit; the subscription's stored duration determines
// quantity, never request metadata or a renewal-worker supplied price.
func subscriptionStock(ctx context.Context, tx *sql.Tx, d c.Definition, sub string, now time.Time) (Product, int, error) {
	var p apps.Product
	var raw []byte
	if e := tx.QueryRowContext(ctx, `SELECT document FROM owned_app_products WHERE id=$1::uuid AND deleted_at IS NULL`, d.ProductID).Scan(&raw); e != nil {
		return Product{}, 0, notFound(e)
	}
	if e := json.Unmarshal(raw, &p); e != nil {
		return Product{}, 0, e
	}
	var days int
	if e := tx.QueryRowContext(ctx, `SELECT cycle_days FROM commerce_v1.subscriptions WHERE id=$1::uuid`, sub).Scan(&days); e != nil {
		return Product{}, 0, e
	}
	if d.CycleDurationDays < 1 || days%d.CycleDurationDays != 0 {
		return Product{}, 0, c.ErrConflict
	}
	quantity := days / d.CycleDurationDays
	if quantity < 1 || quantity > 12 {
		return Product{}, 0, c.ErrConflict
	}
	product := productAdapter(p)
	return product, quantity, checkProductStock(ctx, tx, product, quantity, now)
}

func cancelSubscriptionReservations(ctx context.Context, tx *sql.Tx, s c.Subscription, now time.Time) error {
	rows, e := tx.QueryContext(ctx, `SELECT document FROM commerce_v1.orders WHERE subscription_id=$1::uuid AND actor_id=$2::uuid AND status=0 ORDER BY id FOR UPDATE`, s.ID, s.AccountID)
	if e != nil {
		return e
	}
	orders := []c.Order{}
	for rows.Next() {
		var raw []byte
		var o c.Order
		if e = rows.Scan(&raw); e == nil {
			e = json.Unmarshal(raw, &o)
		}
		if e != nil {
			rows.Close()
			return e
		}
		orders = append(orders, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, o := range orders {
		o.Status = c.Cancelled
		o.UpdatedAt = now
		if e = saveOrder(ctx, tx, o); e != nil {
			return e
		}
		if e = event(ctx, tx, "order.cancelled", o.ID, o, now); e != nil {
			return e
		}
	}
	return nil
}
