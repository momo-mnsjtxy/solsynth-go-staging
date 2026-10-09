// SPDX-License-Identifier: AGPL-3.0-only
package sponsorshippg

import (
	"errors"
	"math/big"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"testing"
)

func TestWeightedIndexExactBoundaries(t *testing.T) {
	a, _ := wallet.ParseAmount("5.001")
	b, _ := wallet.ParseAmount("10.002")
	for _, tc := range []struct {
		roll int64
		want int
	}{{0, 0}, {5000, 0}, {5001, 1}, {15002, 1}} {
		got, e := WeightedIndex([]wallet.Amount{a, b}, func(total *big.Int) (*big.Int, error) {
			if total.String() != "15003" {
				t.Fatal(total)
			}
			return big.NewInt(tc.roll), nil
		})
		if e != nil || got != tc.want {
			t.Fatal(tc, got, e)
		}
	}
	for _, v := range []*big.Int{nil, big.NewInt(-1), big.NewInt(15003)} {
		if _, e := WeightedIndex([]wallet.Amount{a, b}, func(*big.Int) (*big.Int, error) { return v, nil }); !errors.Is(e, s.ErrInvalid) {
			t.Fatal(e)
		}
	}
}
