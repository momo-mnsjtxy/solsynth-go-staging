// SPDX-License-Identifier: AGPL-3.0-only
// Derived from Solsynth/DysonNetwork TimelineService.cs; see PROVENANCE.md.
package sphere

import (
	"math"
	"sort"
	"time"
)

type Mode string

const (
	Personalized Mode = "personalized"
	Top          Mode = "top"
	Latest       Mode = "latest"
)

// Post contains only the values consumed by the deterministic ranking stage.
// Type uses the source enum names: "Article" and "Blog" receive a boost.
// IDs are canonical UUID strings; empty and the zero UUID mean no publisher.
type Post struct {
	ID, PublisherID, Type                   string
	CreatedAt                               time.Time
	PublishedAt                             *time.Time
	ReactionScore, AwardedScore             float64
	ThreadRepliesCount, RealmBoostLevel     int
	IsShadowbanned, PublisherIsShadowbanned bool
	DebugRank                               float64
}

// Signals must be resolved by the caller with the same visibility, account,
// subscription and moderation policy as the source service. These values are
// not trusted HTTP input and this package does not implement authorization.
type Signals struct {
	PersonalizationBonus, PublisherRatingBonus          float64
	AutomatedPenalty, SubscriptionBoost, AutomodPenalty float64
	ShouldHide, ShadowbannedForListing                  bool
}

type Options struct {
	Now                       time.Time
	Take                      int
	Mode                      Mode
	Authenticated, Aggressive bool
}

func timelineTime(p Post) time.Time {
	if p.PublishedAt != nil {
		return *p.PublishedAt
	}
	return p.CreatedAt
}

func hasPublisher(p Post) bool {
	return p.PublisherID != "" && p.PublisherID != "00000000-0000-0000-0000-000000000000"
}

// elapsedSeconds avoids time.Duration's approximately 290-year range limit.
func elapsedSeconds(now, then time.Time) float64 {
	return float64(now.Unix()-then.Unix()) + float64(now.Nanosecond()-then.Nanosecond())/1e9
}

// CalculateBaseRank preserves the source formula, including non-finite scores
// for sufficiently future-dated posts. It does not silently clamp their age.
func CalculateBaseRank(p Post, now time.Time) float64 {
	performance := p.ReactionScore*1.4 + float64(p.ThreadRepliesCount)*0.8 + p.AwardedScore/10
	if p.Type == "Article" || p.Type == "Blog" {
		performance += 1.5
	}
	if p.RealmBoostLevel > 0 {
		performance += float64(p.RealmBoostLevel) * 0.6
	}
	hours := elapsedSeconds(now, timelineTime(p)) / 3600
	return (performance + 5) / math.Pow(hours+1, 1.2)
}

func DecayedInterestScore(score float64, lastInteractedAt *time.Time, now time.Time) float64 {
	if lastInteractedAt == nil {
		return score
	}
	ageDays := math.Max(0, elapsedSeconds(now, *lastInteractedAt)/86400)
	return score * math.Exp(-ageDays/30)
}

type candidate struct {
	post Post
	rank float64
}

// rankGreater follows .NET Double.CompareTo for descending order: NaN is below
// every non-NaN value, and equal scores retain source order.
func rankGreater(a, b float64) bool {
	if math.IsNaN(a) {
		return false
	}
	if math.IsNaN(b) {
		return true
	}
	return a > b
}

// RankPosts ports only the in-memory ranking stage. The caller must first
// enforce visibility and resolve Signals; no data is fetched or persisted.
// The input is not mutated. Mode's zero value selects Personalized.
func RankPosts(posts []Post, signals map[string]Signals, opts Options) []Post {
	if opts.Mode == "" {
		opts.Mode = Personalized
	}
	selected := make([]Post, 0)
	if opts.Take <= 0 {
		return selected
	}
	if opts.Mode == Latest {
		for _, p := range posts {
			if p.IsShadowbanned || (hasPublisher(p) && p.PublisherIsShadowbanned) {
				continue
			}
			p.DebugRank = 0
			selected = append(selected, p)
		}
		sort.SliceStable(selected, func(i, j int) bool { return timelineTime(selected[i]).After(timelineTime(selected[j])) })
		if len(selected) > opts.Take {
			selected = selected[:opts.Take]
		}
		return selected
	}
	candidates := make([]candidate, 0, len(posts))
	for _, p := range posts {
		s := signals[p.ID]
		if s.ShouldHide || s.ShadowbannedForListing {
			continue
		}
		personalization := 0.0
		if opts.Mode == Personalized && opts.Authenticated {
			personalization = s.PersonalizationBonus * 3
		}
		subscription := 0.0
		if opts.Authenticated {
			subscription = s.SubscriptionBoost
		}
		// Preserve source evaluation order as well as the formula: binary64
		// addition/subtraction is not associative and can affect tied ranks.
		rank := CalculateBaseRank(p, opts.Now) + personalization + s.PublisherRatingBonus - s.AutomatedPenalty + subscription - s.AutomodPenalty
		candidates = append(candidates, candidate{p, rank})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return rankGreater(candidates[i].rank, candidates[j].rank) })
	if opts.Mode == Personalized && opts.Authenticated && opts.Aggressive && len(candidates) > opts.Take {
		cutoff := math.Max(0.35, candidates[0].rank*0.18)
		filtered := make([]candidate, 0, len(candidates))
		for _, c := range candidates {
			if c.rank >= cutoff {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) >= min(opts.Take, 3) {
			candidates = filtered
		} else {
			candidates = candidates[:min(len(candidates), max(opts.Take, 3))]
		}
	}
	counts := make(map[string]int)
	for _, c := range candidates {
		if len(selected) >= opts.Take {
			break
		}
		penalty := 0.0
		if hasPublisher(c.post) {
			penalty = float64(counts[c.post.PublisherID]) * 1.35
		}
		c.post.DebugRank = c.rank - penalty
		selected = append(selected, c.post)
		if hasPublisher(c.post) {
			counts[c.post.PublisherID]++
		}
	}
	return selected
}
