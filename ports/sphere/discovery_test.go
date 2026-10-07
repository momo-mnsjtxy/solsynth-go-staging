// SPDX-License-Identifier: AGPL-3.0-only
package sphere

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestDiscoveryScoreDistinctFromTimelineRank(t *testing.T) {
	p := DiscoveryPost{Post: Post{CreatedAt: epoch, ReactionScore: 10, AwardedScore: 50, Type: "Blog", ThreadRepliesCount: 999}, RepliesCount: 5,
		Tags: []DiscoveryLabel{{ID: "tag"}}, Categories: []DiscoveryLabel{{ID: "cat"}}, CollectionIDs: []string{"col"}}
	interests := DiscoveryInterests{Tags: map[string]float64{"tag": 2}, Categories: map[string]float64{"cat": 3}, Collections: map[string]float64{"col": 4}}
	// 1.8 + 2.4 + 2.6 + (1.5 + 1 + .4) + .35
	want := 10.05
	assertNear(t, CalculateDiscoveryPostScore(p, interests, epoch), want)
	published := epoch.Add(-3 * 24 * time.Hour)
	p.PublishedAt = &published
	assertNear(t, CalculateDiscoveryPostScore(p, interests, epoch), want/math.Pow(4, .35))
	published = epoch.Add(24 * time.Hour)
	assertNear(t, CalculateDiscoveryPostScore(p, interests, epoch), want)
	p.ReactionScore = -100
	p.Type = "ordinary"
	assertNear(t, CalculateDiscoveryPostScore(p, interests, epoch), 6.8)
}

func TestDiscoveryAggregationReasonsAndStableTies(t *testing.T) {
	empty := ""
	posts := []DiscoveryPost{
		{Post: Post{PublisherID: "a", CreatedAt: epoch, ReactionScore: 10}, RealmID: "r", Tags: []DiscoveryLabel{{Slug: "first"}}, Categories: []DiscoveryLabel{{Slug: "category"}}},
		{Post: Post{PublisherID: "a", CreatedAt: epoch, ReactionScore: 20}, RealmID: "r", Tags: []DiscoveryLabel{{Name: &empty, Slug: "must-not-fallback"}, {Slug: "second"}, {Slug: "first"}}},
		{Post: Post{PublisherID: "a", CreatedAt: epoch, ReactionScore: 30}, RealmID: "r", Tags: []DiscoveryLabel{{Slug: "   "}, {Slug: "third"}}},
		{Post: Post{PublisherID: "a", CreatedAt: epoch, ReactionScore: 40}, RealmID: "r"},
		{Post: Post{PublisherID: "b", CreatedAt: epoch, ReactionScore: 90}},
	}
	in := DiscoveryInputs{Now: epoch, Publishers: map[string]DiscoveryPublisher{"a": {}, "b": {}}, PublicRealms: map[string]DiscoveryRealm{"r": {BoostLevel: 2}}}
	got := SelectDiscoveryTargets(posts, in)
	if len(got.Publishers) != 2 || got.Publishers[0].ReferenceID != "a" || got.Publishers[1].ReferenceID != "b" {
		t.Fatal(got)
	}
	assertNear(t, got.Publishers[0].Score, 13.5)
	assertNear(t, got.Realms[0].Score, 14.7)
	if !reflect.DeepEqual(got.Publishers[0].Reasons, []string{"first", "second", "third"}) {
		t.Fatal(got)
	}
	if posts[0].DebugRank != 0 || posts[1].Tags[0].Slug != "must-not-fallback" {
		t.Fatal("input mutated")
	}
	// Reasons come from all posts, including those below the top-three cutoff.
	if got.Publishers[0].Reasons[0] != "first" {
		t.Fatal(got)
	}
}

func TestDiscoveryTakeEightBeforePublisherHydration(t *testing.T) {
	posts := make([]DiscoveryPost, 10)
	pubs := make(map[string]DiscoveryPublisher)
	for i := range posts {
		id := fmt.Sprint(i)
		posts[i] = DiscoveryPost{Post: Post{PublisherID: id, CreatedAt: epoch, ReactionScore: float64(10 - i)}}
		pubs[id] = DiscoveryPublisher{}
	}
	delete(pubs, "0")
	pubs["1"] = DiscoveryPublisher{AccountID: "blocked"}
	got := SelectDiscoveryTargets(posts, DiscoveryInputs{Now: epoch, Publishers: pubs, HiddenAccounts: map[string]bool{"blocked": true}})
	if len(got.Publishers) != 6 || got.Publishers[0].ReferenceID != "2" || got.Publishers[5].ReferenceID != "7" {
		t.Fatal(got)
	}
}

func TestDiscoveryExclusionsThresholdAndUnresolvedRecords(t *testing.T) {
	posts := []DiscoveryPost{}
	pubs := map[string]DiscoveryPublisher{}
	realms := map[string]DiscoveryRealm{}
	for _, id := range []string{"own", "subscribed", "hidden", "joined", "hidden-realm", "ok", "missing"} {
		posts = append(posts, DiscoveryPost{Post: Post{PublisherID: id, CreatedAt: epoch, Type: "Article"}, RealmID: id})
		pubs[id] = DiscoveryPublisher{}
		realms[id] = DiscoveryRealm{}
	}
	delete(pubs, "missing")
	delete(realms, "missing")
	in := DiscoveryInputs{Now: epoch, Publishers: pubs, PublicRealms: realms,
		OwnPublishers: map[string]bool{"own": true}, SubscribedPublishers: map[string]bool{"subscribed": true}, HiddenPublishers: map[string]bool{"hidden": true},
		JoinedRealms: map[string]bool{"joined": true}, HiddenRealms: map[string]bool{"hidden-realm": true}}
	got := SelectDiscoveryTargets(posts, in)
	ids := func(s []DiscoverySuggestion) []string {
		out := []string{}
		for _, x := range s {
			out = append(out, x.ReferenceID)
		}
		return out
	}
	if !reflect.DeepEqual(ids(got.Publishers), []string{"joined", "hidden-realm", "ok"}) || !reflect.DeepEqual(ids(got.Realms), []string{"own", "subscribed", "hidden", "ok"}) {
		t.Fatal(got)
	}
	// The strict > .2 threshold applies after the publisher interest adjustment.
	p := []DiscoveryPost{{Post: Post{PublisherID: "p", CreatedAt: epoch}}}
	in = DiscoveryInputs{Now: epoch, Publishers: map[string]DiscoveryPublisher{"p": {}}, Interests: DiscoveryInterests{Publishers: map[string]float64{"p": .5}}}
	if got := SelectDiscoveryTargets(p, in); len(got.Publishers) != 0 {
		t.Fatal(got)
	}
	in.Interests.Publishers["p"] = 1
	if got := SelectDiscoveryTargets(p, in); len(got.Publishers) != 1 || got.Publishers[0].Score != .4 {
		t.Fatal(got)
	}
}

func TestDiscoveryEmptyAndNonFinite(t *testing.T) {
	empty := SelectDiscoveryTargets(nil, DiscoveryInputs{})
	if empty.Publishers == nil || empty.Realms == nil || len(empty.Publishers)+len(empty.Realms) != 0 {
		t.Fatal(empty)
	}
	posts := []DiscoveryPost{
		{Post: Post{PublisherID: "", ReactionScore: 10, CreatedAt: epoch}},
		{Post: Post{PublisherID: "00000000-0000-0000-0000-000000000000", ReactionScore: 10, CreatedAt: epoch}},
		{Post: Post{PublisherID: "nan", ReactionScore: math.NaN(), CreatedAt: epoch}},
		{Post: Post{PublisherID: "inf", ReactionScore: math.Inf(1), CreatedAt: epoch}},
	}
	in := DiscoveryInputs{Now: epoch, Publishers: map[string]DiscoveryPublisher{"nan": {}, "inf": {}}}
	got := SelectDiscoveryTargets(posts, in)
	if len(got.Publishers) != 1 || got.Publishers[0].ReferenceID != "inf" || !math.IsInf(got.Publishers[0].Score, 1) {
		t.Fatal(got)
	}
}

func TestDiscoveryRealmLimitAndNegativeBoost(t *testing.T) {
	posts := []DiscoveryPost{}
	realms := map[string]DiscoveryRealm{}
	for i := 0; i < 10; i++ {
		id := fmt.Sprint(i)
		posts = append(posts, DiscoveryPost{Post: Post{Type: "Article", CreatedAt: epoch}, RealmID: id})
		realms[id] = DiscoveryRealm{}
	}
	realms["0"] = DiscoveryRealm{BoostLevel: -1}
	got := SelectDiscoveryTargets(posts, DiscoveryInputs{Now: epoch, PublicRealms: realms})
	if len(got.Realms) != 8 || got.Realms[0].ReferenceID != "1" || got.Realms[7].ReferenceID != "8" {
		t.Fatal(got)
	}
}

func TestDiscoveryReasonsCategoriesAndNegativeInterest(t *testing.T) {
	p := DiscoveryPost{Post: Post{PublisherID: "p", CreatedAt: epoch}, Tags: []DiscoveryLabel{{ID: "negative", Slug: "tag"}}, Categories: []DiscoveryLabel{{Slug: "tag"}, {Slug: "cat"}, {Slug: "Cat"}}}
	got := discoveryReasons([]DiscoveryPost{p})
	if !reflect.DeepEqual(got, []string{"tag", "cat", "Cat"}) {
		t.Fatal(got)
	}
	score := CalculateDiscoveryPostScore(p, DiscoveryInterests{Tags: map[string]float64{"negative": -2}}, epoch)
	assertNear(t, score, -1.8)
}
