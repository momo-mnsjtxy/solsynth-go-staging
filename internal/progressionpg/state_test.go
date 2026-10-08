// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"encoding/json"
	"os"
	"solsynth.local/go-migration/ports/passport/progression"
	"testing"
	"time"
)

func TestPeriodCalendarBoundaries(t *testing.T) {
	ny, e := time.LoadLocation("America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		schedule, at, key, next string
		loc                     *time.Location
	}{{"daily", "2026-03-08T06:00:00Z", "2026-03-08", "2026-03-09T04:00:00Z", ny}, {"daily", "2026-11-01T05:00:00Z", "2026-11-01", "2026-11-02T05:00:00Z", ny}, {"weekly", "2021-01-01T12:00:00Z", "2020-W53", "2021-01-04T00:00:00Z", time.UTC}, {"monthly", "2024-02-29T23:59:59Z", "2024-02", "2024-03-01T00:00:00Z", time.UTC}, {"daily", "2026-10-08T00:00:00Z", "2026-10-08", "2026-10-09T00:00:00Z", time.UTC}} {
		at, _ := time.Parse(time.RFC3339, c.at)
		key, next := Period(c.schedule, at, c.loc)
		if key != c.key || next.Format(time.RFC3339) != c.next {
			t.Fatalf("%+v => %s %s", c, key, next)
		}
	}
}
func TestStreakIdempotencyOutOfOrderAndGap(t *testing.T) {
	d := progression.Definition{TargetCount: 3, Trigger: progression.Trigger{Mode: "streak"}}
	var p progress
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, n := range []int{0, 0, 1, 0, 2} {
		advance(&p, d, at.AddDate(0, 0, n), time.UTC, false)
	}
	if p.count != 3 || p.streak != 3 || p.best != 3 {
		t.Fatal(p)
	}
	advance(&p, d, at.AddDate(0, 0, 5), time.UTC, false)
	if p.count != 1 || p.streak != 1 || p.best != 3 {
		t.Fatal(p)
	}
	d.Trigger.Mode = "daily_count"
	p = progress{}
	advance(&p, d, at, time.UTC, false)
	advance(&p, d, at.Add(time.Hour), time.UTC, false)
	advance(&p, d, at.AddDate(0, 0, 1), time.UTC, false)
	advance(&p, d, at, time.UTC, false)
	if p.count != 1 {
		t.Fatal("old event reset current day", p)
	}
}
func TestLicensedCatalogIsCompleteAndValidated(t *testing.T) {
	b, e := os.ReadFile("../../ports/passport/progression/catalog.json")
	if e != nil {
		t.Fatal(e)
	}
	var rows []struct {
		Kind       string                 `json:"kind"`
		Definition progression.Definition `json:"definition"`
	}
	if e = json.Unmarshal(b, &rows); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 58 {
		t.Fatal(len(rows))
	}
	for _, r := range rows {
		if e = ValidateDefinition(r.Kind, r.Definition); e != nil {
			t.Fatal(r.Definition.Identifier, e)
		}
		if r.Definition.Identifier == "gots-fired-5" && r.Definition.Trigger.MetaEquals["reaction"] != "thumb_down" {
			t.Fatal("dropped metadata predicate")
		}
	}
}
func TestDefinitionRejectsUnknownBadgesAndUnboundedRewards(t *testing.T) {
	d := progression.Definition{Identifier: "synthetic-test", Title: "Synthetic test", TargetCount: 1, Trigger: progression.Trigger{Actions: []string{"post.create"}, Mode: "count"}, Reward: progression.Reward{Experience: 1000000001}}
	if ValidateDefinition("achievement", d) == nil {
		t.Fatal("unbounded reward")
	}
	d.Reward.Experience = 1
	d.Reward.Badge = &progression.BadgeReward{Type: "invented.badge"}
	if ValidateDefinition("achievement", d) == nil {
		t.Fatal("unknown badge")
	}
}
