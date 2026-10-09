// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"testing"
)

func TestExplicitLevelsAndSourceTenureArithmetic(t *testing.T) {
	for _, c := range []Config{{}, {LevelThresholds: []int64{1}}, {LevelThresholds: []int64{0, 0}}, {LevelThresholds: []int64{0, -1}}, {LevelThresholds: []int64{0, 1000000000000001}}} {
		if ValidateConfig(c) == nil {
			t.Fatal("unverified level policy accepted", c)
		}
	}
	if ValidateConfig(Config{LevelThresholds: []int64{}}) != nil {
		t.Fatal("explicitly absent levels rejected")
	}
	for _, row := range []struct {
		xp    int64
		level int
		next  *int64
	}{{0, 0, i64(10)}, {9, 0, i64(10)}, {10, 1, i64(25)}, {24, 1, i64(25)}, {25, 2, nil}, {100000, 2, nil}} {
		v := project(row.xp, []int64{0, 10, 25})
		if v.Level == nil || *v.Level != row.level || v.ExperienceComplete || v.RecordedExperience != row.xp || v.LevelPolicy == nil || *v.LevelPolicy != "owned.configured.scoped" {
			t.Fatal(v)
		}
		if row.next == nil && v.NextLevelAt != nil || row.next != nil && (v.NextLevelAt == nil || *v.NextLevelAt != *row.next) {
			t.Fatal(v)
		}
	}
	p := project(1000, []int64{})
	if p.Level != nil || p.LevelPolicy != nil || p.LevelFloor != nil || p.NextLevelAt != nil || p.ExperienceComplete {
		t.Fatal("invented total/level", p)
	}
	for perk, want := range []int64{5, 7, 10, 12} {
		if got, e := tenureXP(perk); e != nil || got != want {
			t.Fatal(perk, got, e)
		}
	}
	for _, perk := range []int{-1, 4, 999} {
		if _, e := tenureXP(perk); e != m.ErrUnavailable {
			t.Fatal(perk, e)
		}
	}
}
func i64(v int64) *int64 { return &v }
