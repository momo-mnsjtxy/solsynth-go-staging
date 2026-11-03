// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"testing"

	"solsynth.local/go-migration/internal/application"
)

func TestCheckInStreakGoldConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{nil, true},
		{[]string{"--enable-check-in-streak-gold"}, false},
		{[]string{"--check-in-gold-treasury-account=11111111-1111-4111-8111-111111111111"}, false},
		{[]string{"--enable-check-in-streak-gold", "--check-in-gold-treasury-account=11111111-1111-4111-8111-111111111111", "--check-in-gold-treasury-wallet=22222222-2222-4222-8222-222222222222", "--check-in-gold-max-per-day=5"}, false},
		{[]string{"--enable-check-in-streak-gold", "--check-in-gold-treasury-account=11111111-1111-4111-8111-111111111111", "--check-in-gold-treasury-wallet=22222222-2222-4222-8222-222222222222"}, true},
	} {
		f := flag.NewFlagSet("check-in-gold", flag.ContinueOnError)
		s := progressionExtensionFlags(f)
		if e := f.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		var c application.Config
		if e := s.apply(&c); (e == nil) != tc.ok {
			t.Fatalf("%v: %v", tc.args, e)
		}
		if tc.ok && len(tc.args) > 0 && (c.CheckInStreakGold == nil || c.CheckInStreakGold.Currency != "golds" || c.CheckInStreakGold.MaxPerGrant != 6 || c.ProgressionSourcePoints != nil) {
			t.Fatal("gold policy must be explicit and independent", c.CheckInStreakGold, c.ProgressionSourcePoints)
		}
	}
}
