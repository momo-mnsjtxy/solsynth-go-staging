// SPDX-License-Identifier: AGPL-3.0-only
package rewindpg

import (
	"solsynth.local/go-migration/ports/passport/rewind"
	"testing"
	"time"
)

func TestRewindActivityPreservesInstantBeforeTimezoneConversion(t *testing.T) {
	start, end, _ := rewind.Window(2026)
	loc := time.FixedZone("Synthetic +0200", 2*3600)
	out := rewind.Passport{}
	values := []time.Time{time.Date(2026, 1, 2, 23, 45, 0, 0, time.UTC), time.Date(2026, 1, 2, 22, 30, 0, 0, time.UTC), time.Date(2026, 1, 3, 15, 0, 0, 0, time.UTC), end}
	activityMetrics(&out, values, start, end, loc)
	if out.MostActiveDay == nil || *out.MostActiveDay != "2026-01-02" || out.LatestActiveTime == nil || *out.LatestActiveTime != "01:45" {
		t.Fatal(out)
	}
}
