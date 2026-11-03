// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import "testing"

func TestCheckInStreakGoldCadence(t *testing.T) {
	for _, tc := range []struct {
		days int
		gold int64
	}{{-1, 0}, {0, 0}, {1, 0}, {6, 0}, {7, 1}, {8, 0}, {14, 1}, {28, 1}, {29, 0}, {30, 5}, {60, 5}, {209, 0}, {210, 6}, {420, 6}, {1830, 5}, {1831, 0}, {2100, 6}} {
		if got := checkInStreakGoldReward(tc.days); got != tc.gold {
			t.Errorf("streak %d: gold=%d, want %d", tc.days, got, tc.gold)
		}
	}
}

func TestCheckInStreakGoldRequiresFundedGoldPolicy(t *testing.T) {
	c := SourcePointConfig{TreasuryAccountID: "11111111-1111-4111-8111-111111111111", TreasuryWalletID: "22222222-2222-4222-8222-222222222222", Currency: "golds", MaxPerGrant: 6, MaxPerDay: 100}
	p, e := NewFundedCheckInStreakGold(c)
	if e != nil || new(Repository).WithCheckInStreakGoldPayout(p) != nil {
		t.Fatal(e)
	}
	if new(Repository).WithCheckInStreakGoldPayout(nil) == nil {
		t.Fatal("missing payout accepted")
	}
	for _, invalid := range []SourcePointConfig{
		{TreasuryWalletID: c.TreasuryWalletID, Currency: "golds", MaxPerGrant: 6, MaxPerDay: 100},
		{TreasuryAccountID: c.TreasuryAccountID, TreasuryWalletID: c.TreasuryWalletID, Currency: "points", MaxPerGrant: 6, MaxPerDay: 100},
		{TreasuryAccountID: c.TreasuryAccountID, TreasuryWalletID: c.TreasuryWalletID, Currency: "golds", MaxPerGrant: 5, MaxPerDay: 100},
		{TreasuryAccountID: c.TreasuryAccountID, TreasuryWalletID: c.TreasuryWalletID, Currency: "golds", MaxPerGrant: 6, MaxPerDay: 5},
	} {
		if _, e := NewFundedCheckInStreakGold(invalid); e == nil {
			t.Fatal("invalid treasury policy accepted", invalid)
		}
	}
}
