// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"solsynth.local/go-migration/ports/passport/progression"
	"testing"
)

func TestExplicitBirthdayBackdateAndSeedPolicy(t *testing.T) {
	for _, p := range []CheckInPolicy{{BackdateWindowDays: 367}, {BackdateWindowDays: 1}, {BirthdayExperience: -1}, {PerkProducts: []string{"a", "a"}}} {
		if ValidateCheckInPolicy(p) == nil {
			t.Fatal("invalid policy", p)
		}
	}
	if ValidateCheckInPolicy(CheckInPolicy{BackdateWindowDays: 7, PerkProducts: []string{"owned-perk"}, BirthdayExperience: 50}) != nil {
		t.Fatal("explicit policy rejected")
	}
	seeds, e := progression.Seeds()
	if e != nil || len(seeds) == 0 {
		t.Fatal(e)
	}
	for _, s := range seeds {
		if e = ValidateDefinition(s.Kind, s.Definition); e != nil {
			t.Fatal(s.Definition.Identifier, e)
		}
	}
}
