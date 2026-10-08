// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/develop/apps"
)

// ProductCatalog participates in the product mutation transaction. It may only
// project the canonical product; it must never perform provider network calls.
type ProductCatalog interface {
	SyncProduct(context.Context, *sql.Tx, apps.Product, bool) error
}

func (r *Repository) WithProductCatalog(c ProductCatalog) *Repository {
	r.productCatalog = c
	return r
}
