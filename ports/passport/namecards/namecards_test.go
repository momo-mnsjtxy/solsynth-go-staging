// SPDX-License-Identifier: AGPL-3.0-only
package namecards

import (
	"strings"
	"testing"
)

func TestNameCardSourceBoundsAndReservedReferences(t *testing.T) {
	for _, name := range []string{"AB", "9_a-b", strings.Repeat("A", 256)} {
		q := UseRequest{Target: "account", NewName: name}
		if e := ValidateUse(&q); e != nil {
			t.Fatal(name, e)
		}
	}
	for _, name := range []string{"a", strings.Repeat("A", 257), "has space", "a@example.invalid", "me", "VERIFY", "verification", "10000000-0000-4000-8000-000000000001"} {
		q := UseRequest{Target: "account", NewName: name}
		if e := ValidateUse(&q); e == nil {
			t.Fatal("unsafe account reference", name)
		}
	}
}
func TestNameCardUseRejectsAmbiguousAndForgedEntitlement(t *testing.T) {
	for _, raw := range []string{"", `null`, `{"target":"account","target":"realm","new_name":"valid"}`, `{"target":"account","new_name":"valid","fulfilled_at":"2026-01-01"}`, `{"target":"account","new_name":"valid","amount":0}`, `{"target":"realm","new_name":"realm"}`, `{"target":"account","new_name":null}`} {
		if _, e := decodeUse([]byte(raw)); e == nil {
			t.Fatal(raw)
		}
	}
	if _, e := decodeUse([]byte(`{"target":"account","new_name":"  Aa--_9  ","target_id":null}`)); e != nil {
		t.Fatal(e)
	}
	if ValidKey("has space") || ValidKey(strings.Repeat("x", 129)) {
		t.Fatal("unbounded idempotency key")
	}
}
