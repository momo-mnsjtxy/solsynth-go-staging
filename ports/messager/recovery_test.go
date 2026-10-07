// SPDX-License-Identifier: AGPL-3.0-only
package messager

import (
	"encoding/json"
	"math"
	"math/rand"
	"reflect"
	"testing"
)

func TestRecoveryNormalization(t *testing.T) {
	p := NewRecoveryPlan([]int64{9, 2, 9, 0, -1}, []SequenceRange{
		{8, 5}, {2, 4}, {20, 21}, {21, 24}, {0, 10}, {-3, -1},
	})
	if !reflect.DeepEqual(p.Sequences(), []int64{2, 9}) {
		t.Fatal(p.Sequences())
	}
	if !reflect.DeepEqual(p.Ranges(), []SequenceRange{{2, 8}, {20, 24}}) {
		t.Fatal(p.Ranges())
	}
	for _, n := range []int64{2, 5, 8, 9, 20, 24} {
		if !p.MatchesSequence(n) {
			t.Errorf("missing %d", n)
		}
	}
	for _, n := range []int64{-1, 0, 1, 10, 19, 25} {
		if p.MatchesSequence(n) {
			t.Errorf("unexpected %d", n)
		}
	}
}

func TestTimestampFallbackAndZeroValue(t *testing.T) {
	for _, p := range []RecoveryPlan{{}, NewRecoveryPlan(nil, nil),
		NewRecoveryPlan([]int64{-1, 0}, []SequenceRange{{5, 0}, {-1, -5}})} {
		if p.UsesSequences() || p.MatchesSequence(1) {
			t.Fatal("expected timestamp mode")
		}
		if p.Sequences() == nil || p.Ranges() == nil {
			t.Fatal("expected empty copies")
		}
	}
}

func TestNoAliasing(t *testing.T) {
	ids := []int64{4, 2}
	ranges := []SequenceRange{{9, 7}}
	p := NewRecoveryPlan(ids, ranges)
	if ids[0] != 4 || ranges[0].StartSequence != 9 {
		t.Fatal("mutated input")
	}
	ids[0] = 100
	ranges[0].EndSequence = 100
	p.Sequences()[0] = 100
	p.Ranges()[0].EndSequence = 100
	if !p.MatchesSequence(2) || p.MatchesSequence(100) {
		t.Fatal("plan aliased")
	}
}

func TestInt64BoundaryCompatibility(t *testing.T) {
	p := NewRecoveryPlan(nil, []SequenceRange{{1, math.MaxInt64}, {2, math.MaxInt64}})
	// Upstream's unchecked EndSequence + 1 wraps, retaining the second range.
	if len(p.Ranges()) != 2 {
		t.Fatal(p.Ranges())
	}
	if !p.MatchesSequence(math.MaxInt64) || p.MatchesSequence(math.MinInt64) {
		t.Fatal("boundary selection")
	}
}

func TestSequenceRangeJSON(t *testing.T) {
	var r SequenceRange
	err := json.Unmarshal([]byte(`{"start_sequence":9007199254740993,"end_sequence":9223372036854775807}`), &r)
	if err != nil || r.StartSequence != 9007199254740993 || r.EndSequence != math.MaxInt64 {
		t.Fatal(r, err)
	}
	raw, err := json.Marshal(r)
	if err != nil || string(raw) != `{"start_sequence":9007199254740993,"end_sequence":9223372036854775807}` {
		t.Fatal(string(raw), err)
	}
}

func TestSyntheticSelectionProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 1000; trial++ {
		ids := make([]int64, rng.Intn(20))
		ranges := make([]SequenceRange, rng.Intn(20))
		for i := range ids {
			ids[i] = int64(rng.Intn(100) - 20)
		}
		for i := range ranges {
			ranges[i] = SequenceRange{int64(rng.Intn(100) - 20), int64(rng.Intn(100) - 20)}
		}
		p := NewRecoveryPlan(ids, ranges)
		for n := int64(-20); n <= 100; n++ {
			want := false
			for _, id := range ids {
				want = want || (id > 0 && id == n)
			}
			for _, r := range ranges {
				a, b := r.StartSequence, r.EndSequence
				if a > b {
					a, b = b, a
				}
				want = want || (a > 0 && n >= a && n <= b)
			}
			if p.MatchesSequence(n) != want {
				t.Fatalf("trial %d sequence %d", trial, n)
			}
		}
	}
}
