// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"encoding/json"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"testing"
)

func TestCyclePriceExactBoundedClientMultiples(t *testing.T) {
	base, _ := wallet.ParseAmount("2.123")
	d := Definition{BasePrice: base, UnitPrice: json.Number("2.1239"), CycleDurationDays: 30}
	for _, tc := range []struct {
		days   int
		amount string
	}{{30, "2.123"}, {60, "4.247"}, {360, "25.486"}} {
		days, amount, e := CyclePrice(d, &tc.days)
		if e != nil || days != tc.days || amount.String() != tc.amount {
			t.Fatal(tc, days, amount, e)
		}
	}
	for _, days := range []int{-1, 0, 1, 31, 365, 390, 1000000} {
		if _, _, e := CyclePrice(d, &days); e == nil {
			t.Fatal("unsupported cycle", days)
		}
	}
	d.CycleDurationDays = 365
	if days, _, e := CyclePrice(d, nil); e != nil || days != 365 {
		t.Fatal(days, e)
	}
	d.UnitPrice = ""
	d.CycleDurationDays = 30
	n := 60
	if _, amount, e := CyclePrice(d, &n); e != nil || amount.String() != "4.246" {
		t.Fatal("platform exact units", amount, e)
	}
}
