// SPDX-License-Identifier: AGPL-3.0-only
package rewind

import (
	"testing"
	"time"
)

func TestRewindSourceWindowAndLeapStreak(t *testing.T) {
	start, end, e := Window(2024)
	if e != nil || start.Format("2006-01-02") != "2023-12-26" || end.Format("2006-01-02") != "2024-12-26" {
		t.Fatal(start, end, e)
	}
	days := []time.Time{}
	for _, raw := range []string{"2024-02-28", "2024-02-29", "2024-03-01", "2024-03-01", "2024-03-03"} {
		d, _ := time.Parse("2006-01-02", raw)
		days = append(days, d)
	}
	streak, completeness := CheckInMetrics(days)
	if streak != 3 || completeness != float64(4)/365 {
		t.Fatal(streak, completeness)
	}
	for _, year := range []int{0, 1900, 2200} {
		if _, _, e := Window(year); e == nil {
			t.Fatal(year)
		}
	}
}
