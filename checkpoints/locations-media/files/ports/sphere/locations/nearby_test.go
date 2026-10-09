// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"math"
	"testing"
	"time"
)

func TestNearbyObservationBounds(t *testing.T) {
	now := time.Unix(120, 0)
	base := Observation{Token: "0123456789abcdef0123456789abcdef", Slot: 2, AvgRSSI: -50, SeenCount: 1}
	if e := ValidateObservations([]Observation{base}, now); e != nil {
		t.Fatal(e)
	}
	for _, modify := range []func(*Observation){func(v *Observation) { v.Token = "" }, func(v *Observation) { v.Slot = math.MaxInt64 }, func(v *Observation) { v.SeenCount = 0 }, func(v *Observation) { v.AvgRSSI = 1 }, func(v *Observation) { v.DurationMS = 600001 }, func(v *Observation) { future := now.Add(time.Second); v.LastSeenAt = &future }, func(v *Observation) { past := now.Add(-time.Second); v.FirstSeenAt = &past }} {
		v := base
		modify(&v)
		if ValidateObservations([]Observation{v}, now) == nil {
			t.Fatal("invalid observation accepted", v)
		}
	}
}
