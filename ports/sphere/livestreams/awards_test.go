// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"encoding/json"
	"testing"
)

func TestAwardPointsExactLedgerAndShare(t *testing.T) {
	for _, raw := range []string{"0", "-1", "0.001", "0.009", "1.001", "1.0001", "1e-4", "null", `"10"`} {
		var a AwardPoints
		if e := json.Unmarshal([]byte(raw), &a); e == nil {
			t.Fatal("accepted unrepresentable award", raw, a)
		}
	}
	for _, v := range []struct {
		raw, amount, share string
		seconds            int64
	}{{"0.01", "0.010", "0.009", 0}, {"10", "10.000", "9.000", 20}, {"1e2", "100.000", "90.000", 200}, {"10.50", "10.500", "9.450", 21}} {
		var a AwardPoints
		if e := json.Unmarshal([]byte(v.raw), &a); e != nil {
			t.Fatal(v, e)
		}
		share, e := a.Share()
		seconds, x := a.HighlightSeconds()
		if e != nil || x != nil || a.Amount().String() != v.amount || share.String() != v.share || seconds != v.seconds {
			t.Fatal(v, a, share, seconds, e, x)
		}
	}
}
