// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"solsynth.local/go-migration/ports/passport/progression"
	"testing"
	"time"
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

func TestSeedFriendTimingAndWithinDayCount(t *testing.T) {
	d := progression.Definition{Identifier: "hidden-speed-friend", IsSeedManaged: true, TargetCount: 1, Trigger: progression.Trigger{Actions: []string{"relationship.friend_accept"}, Mode: "count"}}
	for _, c := range []struct {
		elapsed string
		want    bool
	}{{"0s", true}, {"1m", true}, {"1m0.000001s", false}, {"-1s", false}, {"", false}} {
		if got := matches(d, canonicalEvent{kind: "relationship.friend_accept", at: time.Now(), meta: map[string]string{"request_elapsed": c.elapsed}}, time.UTC); got != c.want {
			t.Fatalf("elapsed %q: %v", c.elapsed, got)
		}
	}
	d.Identifier, d.TargetCount, d.Trigger.Mode = "hidden-social-butterfly-day", 5, "streak"
	var p progress
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for range 4 {
		advance(&p, d, at, time.UTC, false)
	}
	if p.count != 4 {
		t.Fatal("same-day friends lost", p)
	}
	advance(&p, d, at.AddDate(0, 0, 1), time.UTC, false)
	if p.count != 1 {
		t.Fatal("previous day's friends carried forward", p)
	}
	for range 4 {
		advance(&p, d, at.AddDate(0, 0, 1), time.UTC, false)
	}
	if p.count != 5 {
		t.Fatal("same-day achievement not complete", p)
	}
}
