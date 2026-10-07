// SPDX-License-Identifier: AGPL-3.0-only
// Adapted from Solsynth/DysonNetwork ChatService.cs, revision
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-07.

// Package messager contains the pure sequence-recovery portion of room sync.
// It does not authorize access, query a database, or deliver realtime messages.
package messager

import "sort"

// SequenceRange is an inclusive pair in the existing room sync JSON contract.
type SequenceRange struct {
	StartSequence int64 `json:"start_sequence"`
	EndSequence   int64 `json:"end_sequence"`
}

// RecoveryPlan is an immutable normalized selection. Its zero value requests
// timestamp mode. Callers must separately enforce room and thread constraints.
type RecoveryPlan struct {
	sequences []int64
	ranges    []SequenceRange
}

// NewRecoveryPlan ports GetSyncDataAsync's selection normalization and
// NormalizeSequenceRanges. Invalid nonpositive ranges are discarded whole,
// after reversing bounds where necessary; they are not clipped to one.
func NewRecoveryPlan(sequences []int64, ranges []SequenceRange) RecoveryPlan {
	p := RecoveryPlan{sequences: make([]int64, 0), ranges: make([]SequenceRange, 0)}
	seen := make(map[int64]struct{}, len(sequences))
	for _, sequence := range sequences {
		if sequence <= 0 {
			continue
		}
		if _, found := seen[sequence]; found {
			continue
		}
		seen[sequence] = struct{}{}
		p.sequences = append(p.sequences, sequence)
	}
	sort.Slice(p.sequences, func(i, j int) bool { return p.sequences[i] < p.sequences[j] })
	normalized := make([]SequenceRange, 0, len(ranges))
	for _, r := range ranges {
		if r.StartSequence > r.EndSequence {
			r.StartSequence, r.EndSequence = r.EndSequence, r.StartSequence
		}
		if r.StartSequence > 0 {
			normalized = append(normalized, r)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].StartSequence == normalized[j].StartSequence {
			return normalized[i].EndSequence < normalized[j].EndSequence
		}
		return normalized[i].StartSequence < normalized[j].StartSequence
	})
	for _, r := range normalized {
		if len(p.ranges) == 0 {
			p.ranges = append(p.ranges, r)
			continue
		}
		previous := &p.ranges[len(p.ranges)-1]
		// Preserve upstream's unchecked signed addition, including MaxInt64.
		if r.StartSequence <= previous.EndSequence+1 {
			if r.EndSequence > previous.EndSequence {
				previous.EndSequence = r.EndSequence
			}
		} else {
			p.ranges = append(p.ranges, r)
		}
	}
	return p
}

// UsesSequences reports whether to replace timestamp filtering with sequence
// recovery. Supplying only invalid values still selects timestamp mode.
func (p RecoveryPlan) UsesSequences() bool { return len(p.sequences) > 0 || len(p.ranges) > 0 }

// MatchesSequence implements BuildSequenceRecoveryPredicate's OR selection.
// False in timestamp mode does not mean a message should be excluded; the
// caller must then apply the original strict created_at timestamp filter.
func (p RecoveryPlan) MatchesSequence(sequence int64) bool {
	i := sort.Search(len(p.sequences), func(i int) bool { return p.sequences[i] >= sequence })
	if i < len(p.sequences) && p.sequences[i] == sequence {
		return true
	}
	for _, r := range p.ranges {
		if sequence >= r.StartSequence && sequence <= r.EndSequence {
			return true
		}
	}
	return false
}

// Sequences returns an independent, nonnil copy of the sorted exact IDs.
func (p RecoveryPlan) Sequences() []int64 { return append([]int64{}, p.sequences...) }

// Ranges returns an independent, nonnil copy of the normalized ranges.
func (p RecoveryPlan) Ranges() []SequenceRange { return append([]SequenceRange{}, p.ranges...) }
