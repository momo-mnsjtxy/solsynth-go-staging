// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"encoding/json"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
)

func TestRecurringDefinitionScopeAndImmutableTerms(t *testing.T) {
	p := apps.Product{ID: "00000000-0000-4000-8000-000000000001", AppID: "00000000-0000-4000-8000-000000000002", Identifier: "same-sku", DisplayName: "Synthetic", Currency: "points", Price: json.Number("2.1239"), Recurrence: "monthly", State: apps.ProductState{IsEnabled: true, StockMode: "unlimited"}}
	d, e := recurringDefinition(p, "00000000-0000-4000-8000-000000000003")
	if e != nil || d.CycleDurationDays != 30 || d.BasePrice.String() != "2.123" || d.ProductTerms == "" {
		t.Fatal(d, e)
	}
	p.DisplayName = "New label"
	renamed, e := recurringDefinition(p, d.PayeeWalletID)
	if e != nil || d.ProductTerms != renamed.ProductTerms {
		t.Fatal("cosmetic change invalidated economics", e)
	}
	p.Price = "3"
	changed, _ := recurringDefinition(p, d.PayeeWalletID)
	if changed.ProductTerms == d.ProductTerms {
		t.Fatal("price mutation retained terms")
	}
	p.ID = "00000000-0000-4000-8000-000000000004"
	other, _ := recurringDefinition(p, d.PayeeWalletID)
	if other.Identifier == d.Identifier {
		t.Fatal("same SKU collided across products")
	}
	p.Fulfillment = &apps.Fulfillment{IsAddressRequired: true}
	if _, e = recurringDefinition(p, d.PayeeWalletID); e != c.ErrUnavailable {
		t.Fatal("address adapter fabricated", e)
	}
}
