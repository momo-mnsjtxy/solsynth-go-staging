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

type CustomOrderManager interface {
	RequireCustomOrderManager(context.Context, *sql.Tx, apps.Scope) (apps.Client, error)
}

// ManagedOrders shares canonical commerce storage/settlement while retaining a
// distinct, account-authorized entrance. It never manufactures app credentials.
type ManagedOrders struct {
	commerce *Repository
	manager  CustomOrderManager
}

// ManagedOrdersReady fails closed until the explicit identifier migration has
// made the authored 1024-byte invoice bound durable. It never performs DDL.
func (r *Repository) ManagedOrdersReady(ctx context.Context) error {
	if r == nil || r.db == nil {
		return c.ErrUnavailable
	}
	var ready bool
	if e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='commerce_v1' AND table_name='orders' AND column_name='product_identifier' AND data_type='character varying' AND character_maximum_length=1024)`).Scan(&ready); e != nil {
		return e
	}
	if !ready {
		return c.ErrUnavailable
	}
	return nil
}

func NewManagedOrders(r *Repository, manager CustomOrderManager) *ManagedOrders {
	return &ManagedOrders{commerce: r, manager: manager}
}

func (r *ManagedOrders) CreateCustomOrder(ctx context.Context, scope apps.Scope, req apps.CustomOrderRequest, key string) (out c.Order, e error) {
	if r == nil || r.commerce == nil || r.manager == nil {
		return out, c.ErrUnavailable
	}
	amount, hours, e := apps.ValidateCustomOrder(req)
	if e != nil || len(key) > 128 {
		return out, apps.ErrInvalid
	}
	req.Amount = apps.CustomOrderAmount(amount.String())
	req.ExpirationHours = &hours
	if req.Meta == nil {
		req.Meta = map[string]json.RawMessage{}
	}
	fp, e := fingerprint(req)
	if e != nil {
		return out, e
	}
	tx, e := r.commerce.begin(ctx, "", "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	app, e := r.manager.RequireCustomOrderManager(ctx, tx, scope)
	if e != nil {
		return out, e
	}
	owner := "managed-app:" + app.AppID
	prior, e := idem(ctx, tx, owner, key, "custom-order", fp)
	if e != nil {
		return out, e
	}
	if prior != "" {
		out, e = order(ctx, tx, prior, false)
		if e != nil {
			return out, e
		}
		if out.AppIdentifier != "developer.app:"+app.AppID {
			return c.Order{}, c.ErrConflict
		}
	} else {
		// The configured merchant belongs to this canonical publisher. Hold the
		// binding and wallet through commit; capture the beneficiary in the order.
		var payee string
		e = tx.QueryRowContext(ctx, `SELECT m.wallet_id::text FROM commerce_v1.merchants m JOIN wallet_v1.wallets w ON w.id=m.wallet_id JOIN owned_publishers p ON p.id=$2 WHERE m.app_id=$1 AND w.deleted_at IS NULL AND p.deleted_at IS NULL AND p.type=0 AND p.realm_id IS NULL AND p.account_id=w.account_id FOR SHARE OF m,w`, app.AppID, app.PublisherID).Scan(&payee)
		if e != nil {
			return out, notFound(e)
		}
		now := r.commerce.now().UTC()
		oid, e := id()
		if e != nil {
			return out, e
		}
		out = c.Order{ID: oid, Status: c.Unpaid, Currency: req.Currency, Amount: amount, AppIdentifier: "developer.app:" + app.AppID, ProductIdentifier: &req.Identifier, Remarks: req.Remarks, Meta: req.Meta, PayeeWalletID: payee, Items: []c.OrderItem{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(time.Duration(hours) * time.Hour)}
		if e = insertOrder(ctx, tx, out, app.AppID, "", "", ""); e != nil {
			return out, e
		}
		if e = recordIdem(ctx, tx, owner, key, "custom-order", fp, out.ID); e != nil {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_apps_audit(actor_id,app_id,action,target_id,occurred_at)VALUES($1,$2,'developers.apps.orders.create',$3,$4)`, scope.Actor, app.AppID, out.ID, now); e != nil {
			return out, e
		}
	}
	// A merchant/idempotency/outbox/audit wait can cross session expiry, a
	// scheduled disable, or a concurrent owner-policy commit. Roll back all work.
	current, e := r.manager.RequireCustomOrderManager(ctx, tx, scope)
	if e != nil {
		return c.Order{}, e
	}
	if current != app {
		return c.Order{}, c.ErrConflict
	}
	return c.RedactShipping(out), tx.Commit()
}

var _ apps.CustomOrderStore = (*ManagedOrders)(nil)
