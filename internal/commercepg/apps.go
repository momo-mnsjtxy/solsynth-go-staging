// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

// AppAdapters composes the canonical app identity and product tables; no second
// app catalog, app secret table, or ledger is introduced by commerce.
func AppAdapters(r *apppg.Repository) Adapters {
	return Adapters{
		Client: func(ctx context.Context, tx *sql.Tx, ref, token, permission string) (AppClient, error) {
			v, e := r.RequireClient(ctx, tx, ref, token, permission)
			return AppClient{AppID: v.AppID, PublisherID: v.PublisherID}, appError(e)
		},
		Product: func(ctx context.Context, tx *sql.Tx, app, identifier string) (Product, error) {
			p, e := r.LockProduct(ctx, tx, app, identifier)
			if e != nil {
				return Product{}, appError(e)
			}
			return Product{ID: p.ID, Identifier: p.Identifier, Currency: p.Currency, Price: p.Price, Enabled: p.State.IsEnabled, StockMode: p.State.StockMode, StockQuantity: p.State.StockQuantity, Recurrence: p.Recurrence, RequiresAddress: p.Fulfillment != nil && p.Fulfillment.IsAddressRequired, Catalog: p}, nil
		},
	}
}
func appError(e error) error {
	switch {
	case errors.Is(e, apps.ErrNotFound):
		return c.ErrNotFound
	case errors.Is(e, apps.ErrForbidden):
		return c.ErrForbidden
	case errors.Is(e, apps.ErrUnavailable):
		return c.ErrUnavailable
	case errors.Is(e, apps.ErrInvalid):
		return c.ErrInvalid
	}
	return e
}
