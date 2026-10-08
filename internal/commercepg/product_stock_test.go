// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"testing"
	"time"
)

func TestStockWindowUTCAndManual(t *testing.T) {
	now := time.Date(2026, 10, 11, 13, 14, 15, 0, time.FixedZone("synthetic", 9*3600))
	for _, tc := range []struct{ mode, want string }{{"daily", "2026-10-11T00:00:00Z"}, {"weekly", "2026-10-05T00:00:00Z"}, {"monthly", "2026-10-01T00:00:00Z"}, {"yearly", "2026-01-01T00:00:00Z"}} {
		start, e := stockWindow(tc.mode, nil, now)
		if e != nil || start == nil || start.Format(time.RFC3339) != tc.want {
			t.Fatal(tc, start, e)
		}
	}
	for _, mode := range []string{"manual", "unlimited"} {
		if start, e := stockWindow(mode, nil, now); e != nil || start != nil {
			t.Fatal(mode, start, e)
		}
	}
	manual := now.Add(-time.Hour)
	if start, e := stockWindow("manual", &manual, now); e != nil || start == nil || !start.Equal(manual) {
		t.Fatal(start, e)
	}
	if _, e := stockWindow("unknown", nil, now); e == nil {
		t.Fatal("unknown stock mode accepted")
	}
	leap := time.Date(2028, 3, 1, 0, 0, 0, 0, time.UTC)
	start, e := stockWindow("monthly", nil, leap.Add(-time.Nanosecond))
	if e != nil || start.Day() != 1 || start.Month() != time.February {
		t.Fatal(start, e)
	}
}
