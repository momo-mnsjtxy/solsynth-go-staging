// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShippingProjectionDoesNotExposeOrMutateBuyerDestination(t *testing.T) {
	o := Order{Meta: map[string]json.RawMessage{"fulfillment": json.RawMessage(`{"address_contact_id":"synthetic-private-id","address_snapshot":"Synthetic Private Road","requires_address":true}`), "address_contact_id": json.RawMessage(`"synthetic-private-id"`), "address_snapshot": json.RawMessage(`"Synthetic Private Road"`), "other": json.RawMessage(`{"stable":true}`)}}
	v := RedactShipping(o)
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "Private Road") || strings.Contains(string(raw), "private-id") {
		t.Fatal(string(raw))
	}
	v.Meta["other"][0] = '['
	if o.Meta["other"][0] != '{' {
		t.Fatal("projection aliased canonical metadata")
	}
	if !strings.Contains(string(o.Meta["fulfillment"]), "Private Road") {
		t.Fatal("projection erased canonical snapshot")
	}
}
