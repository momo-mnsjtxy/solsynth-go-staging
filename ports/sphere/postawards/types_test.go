// SPDX-License-Identifier: AGPL-3.0-only
package postawards

import (
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"testing"
)

func TestCapturedPayoutPolicies(t *testing.T) {
	for _, v := range []struct {
		amount, policy, want string
		attitude             int
	}{{"0.001", MerchantFull, "0.001", 0}, {"0.001", Documented80, "0.000", 0}, {"1.239", Documented80, "0.991", 0}, {"1.239", MerchantFull, "1.239", 0}, {"1.239", MerchantFull, "0.000", 2}} {
		a, _ := wallet.ParseAmount(v.amount)
		got, e := Share(a, v.attitude, v.policy)
		if e != nil || got.String() != v.want {
			t.Fatal(v, got, e)
		}
	}
	a, _ := wallet.ParseAmount("1")
	if _, e := Share(a, 1, MerchantFull); e == nil {
		t.Fatal("neutral accepted")
	}
	if _, e := Share(a, 0, "implicit"); e == nil {
		t.Fatal("unknown policy accepted")
	}
}
