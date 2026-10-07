// SPDX-License-Identifier: AGPL-3.0-only
package sphere

import (
	"math"
	"reflect"
	"testing"
	"time"
)

var epoch = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func assertNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("got %.15g, want %.15g", got, want)
	}
}

func TestBaseRankSourceFormula(t *testing.T) {
	post := Post{CreatedAt: epoch, ReactionScore: 10, ThreadRepliesCount: 5, AwardedScore: 20, Type: "Article", RealmBoostLevel: 2}
	assertNear(t, CalculateBaseRank(post, epoch), 27.7)
	assertNear(t, CalculateBaseRank(post, epoch.Add(time.Hour)), 27.7/math.Pow(2, 1.2))
	published := epoch.Add(-time.Hour)
	post.PublishedAt = &published
	assertNear(t, CalculateBaseRank(post, epoch), 27.7/math.Pow(2, 1.2))
	post.Type = "Blog"
	assertNear(t, CalculateBaseRank(post, epoch), 27.7/math.Pow(2, 1.2))
}

func TestRankSignalsAndPublisherPenaltyDoesNotReorder(t *testing.T) {
	posts := []Post{{ID: "a", PublisherID: "p", CreatedAt: epoch}, {ID: "b", PublisherID: "p", CreatedAt: epoch}, {ID: "c", PublisherID: "q", CreatedAt: epoch}}
	signals := map[string]Signals{"a": {PublisherRatingBonus: 2}, "b": {PublisherRatingBonus: 1}, "c": {PublisherRatingBonus: 0.5}}
	got := RankPosts(posts, signals, Options{Now: epoch, Take: 3, Mode: Top})
	if got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatal(got)
	}
	assertNear(t, got[1].DebugRank, 4.65)
	assertNear(t, got[2].DebugRank, 5.5)
	if posts[0].DebugRank != 0 {
		t.Fatal("mutated caller's post")
	}
}

func TestPersonalizedAndTop(t *testing.T) {
	posts := []Post{{ID: "a", CreatedAt: epoch}, {ID: "b", CreatedAt: epoch}}
	signals := map[string]Signals{"a": {PersonalizationBonus: 2, PublisherRatingBonus: 1, AutomatedPenalty: 2.5, SubscriptionBoost: 1.5, AutomodPenalty: 1}}
	for _, tc := range []struct {
		mode Mode
		auth bool
		want float64
	}{{Personalized, true, 10}, {Top, true, 4}, {Personalized, false, 2.5}} {
		got := RankPosts(posts, signals, Options{Now: epoch, Take: 2, Mode: tc.mode, Authenticated: tc.auth})
		for _, p := range got {
			if p.ID == "a" {
				assertNear(t, p.DebugRank, tc.want)
			}
		}
	}
}

func TestLatestAndStableTies(t *testing.T) {
	published := epoch.Add(time.Hour)
	posts := []Post{{ID: "a", CreatedAt: epoch}, {ID: "b", CreatedAt: epoch}, {ID: "c", CreatedAt: epoch, PublishedAt: &published}, {ID: "d", CreatedAt: epoch, IsShadowbanned: true}, {ID: "e", PublisherID: "p", CreatedAt: epoch, PublisherIsShadowbanned: true}, {ID: "f", CreatedAt: epoch, PublisherIsShadowbanned: true}}
	got := RankPosts(posts, map[string]Signals{"c": {ShouldHide: true}}, Options{Now: epoch, Mode: Latest, Take: 20})
	ids := []string{}
	for _, p := range got {
		ids = append(ids, p.ID)
		assertNear(t, p.DebugRank, 0)
	}
	if !reflect.DeepEqual(ids, []string{"c", "a", "b", "f"}) {
		t.Fatal(ids)
	}
}

func TestAggressiveThresholdAndFallback(t *testing.T) {
	posts := []Post{{ID: "a", CreatedAt: epoch}, {ID: "b", CreatedAt: epoch}, {ID: "c", CreatedAt: epoch}, {ID: "d", CreatedAt: epoch}, {ID: "e", CreatedAt: epoch}}
	signals := map[string]Signals{"a": {PublisherRatingBonus: 95}, "b": {PublisherRatingBonus: 20}, "c": {PublisherRatingBonus: 15}}
	opts := Options{Now: epoch, Mode: Personalized, Take: 4, Authenticated: true, Aggressive: true}
	if got := RankPosts(posts, signals, opts); len(got) != 3 {
		t.Fatalf("threshold should retain three, got %v", got)
	}
	delete(signals, "c")
	if got := RankPosts(posts, signals, opts); len(got) != 4 {
		t.Fatalf("fallback should retain four, got %v", got)
	}
	opts.Aggressive = false
	if got := RankPosts(posts, signals, opts); len(got) != 4 {
		t.Fatal(got)
	}
}

func TestHiddenNonpositiveTakeAndNaN(t *testing.T) {
	posts := []Post{{ID: "nan", CreatedAt: epoch.Add(2 * time.Hour)}, {ID: "hidden", CreatedAt: epoch}, {ID: "ok", CreatedAt: epoch}}
	signals := map[string]Signals{"hidden": {ShadowbannedForListing: true}}
	got := RankPosts(posts, signals, Options{Now: epoch, Mode: Top, Take: 10})
	if len(got) != 2 || got[0].ID != "ok" || !math.IsNaN(got[1].DebugRank) {
		t.Fatal(got)
	}
	for _, take := range []int{-1, 0} {
		if got := RankPosts(posts, nil, Options{Now: epoch, Take: take}); len(got) != 0 {
			t.Fatal(got)
		}
	}
}

func TestInterestDecay(t *testing.T) {
	assertNear(t, DecayedInterestScore(-6, nil, epoch), -6)
	past := epoch.Add(-30 * 24 * time.Hour)
	assertNear(t, DecayedInterestScore(10, &past, epoch), 10/math.E)
	future := epoch.Add(time.Hour)
	assertNear(t, DecayedInterestScore(10, &future, epoch), 10)
}
