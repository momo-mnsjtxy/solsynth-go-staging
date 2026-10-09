// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"encoding/json"
	"testing"
)

func TestShippingSelectionRequiresExplicitOriginalFields(t *testing.T) {
	id := `"00000000-0000-4000-8000-000000000001"`
	for _, meta := range []map[string]json.RawMessage{nil, {"address_contact_id": json.RawMessage(id)}, {"address_contact_id": json.RawMessage(`"bad"`), "address_snapshot": json.RawMessage(`"Synthetic"`)}, {"address_contact_id": json.RawMessage(id), "address_snapshot": json.RawMessage(`null`)}, {"address_contact_id": json.RawMessage(id), "address_snapshot": json.RawMessage(`"   "`)}, {"address_contact_id": json.RawMessage(id), "address_snapshot": json.RawMessage(`{"content":"Synthetic"}`)}} {
		if _, e := shippingSelection(meta); e == nil {
			t.Fatal("invalid unverified selector accepted")
		}
	}
	if got, e := shippingSelection(map[string]json.RawMessage{"address_contact_id": json.RawMessage(id), "address_snapshot": json.RawMessage(`"Synthetic placeholder"`)}); e != nil || got != "00000000-0000-4000-8000-000000000001" {
		t.Fatal(got, e)
	}
}
