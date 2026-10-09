// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"context"
	"encoding/json"
	"time"
)

const FulfillmentReadScope = "orders.fulfillment.read"

type Fulfillment struct {
	AddressContactID string `json:"address_contact_id,omitempty"`
	AddressSnapshot  string `json:"address_snapshot,omitempty"`
	RequiresAddress  bool   `json:"requires_address"`
}

// MerchantFulfillment omits saved-contact IDs, payer account/wallet and unrelated
// private metadata. Finished is merchant-declared completion, not carrier proof.
type MerchantFulfillment struct {
	OrderID         string      `json:"order_id"`
	Status          OrderStatus `json:"status"`
	Items           []OrderItem `json:"items"`
	AddressSnapshot string      `json:"address_snapshot"`
	CapturedAt      time.Time   `json:"captured_at"`
}
type FulfillmentStore interface {
	OrderFulfillment(context.Context, string, Credentials) (MerchantFulfillment, error)
}

// RedactShipping returns a distinct metadata map so projections cannot mutate
// a stored order or an in-flight transaction's canonical destination.
func RedactShipping(o Order) Order {
	meta := map[string]json.RawMessage{}
	for k, v := range o.Meta {
		if k != "address_contact_id" && k != "address_snapshot" && k != "fulfillment" {
			meta[k] = append(json.RawMessage(nil), v...)
		}
	}
	if _, ok := o.Meta["fulfillment"]; ok {
		meta["fulfillment"] = json.RawMessage(`{"requires_address":true}`)
	}
	o.Meta = meta
	return o
}
