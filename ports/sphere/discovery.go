// SPDX-License-Identifier: AGPL-3.0-only
// Derived from licensed TimelineService.cs; see discovery-PROVENANCE.md.
package sphere

import (
	"math"
	"sort"
	"strings"
	"time"
)

// DiscoveryLabel preserves null-name fallback: a present empty name does not
// fall back to Slug. IDs, names and membership are resolved by trusted callers.
type DiscoveryLabel struct {
	ID   string
	Name *string
	Slug string
}

// DiscoveryPost contains the already-visible candidate projection. RepliesCount
// is the source discovery counter, distinct from ranking's ThreadRepliesCount.
// CollectionIDs must already be distinct, as in GetPostCollectionMapAsync.
type DiscoveryPost struct {
	Post
	RealmID          string
	RepliesCount     int
	Tags, Categories []DiscoveryLabel
	CollectionIDs    []string
}

type DiscoveryInterests struct{ Tags, Categories, Collections, Publishers map[string]float64 }

type DiscoveryPublisher struct{ AccountID string }
type DiscoveryRealm struct{ BoostLevel int }

// DiscoveryInputs are resolved snapshots, not request parameters or permission
// checks. Publishers contains only records surviving the source database query
// filters; PublicRealms must contain only public realms visible to this caller.
// Missing records are omitted. HiddenAccounts combines blocked and muted IDs.
type DiscoveryInputs struct {
	Now                                                   time.Time
	Interests                                             DiscoveryInterests
	Publishers                                            map[string]DiscoveryPublisher
	PublicRealms                                          map[string]DiscoveryRealm
	OwnPublishers, SubscribedPublishers, HiddenPublishers map[string]bool
	JoinedRealms, HiddenRealms, HiddenAccounts            map[string]bool
}

// DiscoverySuggestion is a domain selection result, not a transport response.
// Hydration of labels, safe public fields and account suggestions is unported.
type DiscoverySuggestion struct {
	ReferenceID string
	Score       float64
	Reasons     []string
}
type DiscoverySelection struct{ Publishers, Realms []DiscoverySuggestion }

func CalculateDiscoveryPostScore(p DiscoveryPost, interests DiscoveryInterests, now time.Time) float64 {
	var tags, categories, collections float64
	for _, tag := range p.Tags {
		tags += interests.Tags[tag.ID]
	}
	for _, category := range p.Categories {
		categories += interests.Categories[category.ID]
	}
	for _, id := range p.CollectionIDs {
		collections += interests.Collections[id]
	}
	engagement := math.Max(0, p.ReactionScore*0.15+p.AwardedScore/50+float64(p.RepliesCount)*0.08)
	article := 0.0
	if p.Type == "Article" || p.Type == "Blog" {
		article = 0.35
	}
	ageDays := math.Max(0, elapsedSeconds(now, timelineTime(p.Post))/86400)
	freshness := 1 / math.Pow(ageDays+1, 0.35)
	return (tags*0.9 + categories*0.8 + collections*0.65 + engagement + article) * freshness
}

func discoveryReasons(posts []DiscoveryPost) []string {
	labels := make([]string, 0, 3)
	seen := make(map[string]bool)
	// Upstream concatenates all tag labels before all category labels.
	for _, categories := range []bool{false, true} {
		for _, p := range posts {
			source := p.Tags
			if categories {
				source = p.Categories
			}
			for _, item := range source {
				label := item.Slug
				if item.Name != nil {
					label = *item.Name
				}
				if strings.TrimSpace(label) == "" || seen[label] {
					continue
				}
				seen[label] = true
				labels = append(labels, label)
				if len(labels) == 3 {
					return labels
				}
			}
		}
	}
	return labels
}

// SelectDiscoveryTargets implements the publisher and realm lists before the
// enclosing profile's separate Take(3). Each list has at most eight entries.
// Stable ties preserve first candidate occurrence. No input is modified.
func SelectDiscoveryTargets(posts []DiscoveryPost, in DiscoveryInputs) DiscoverySelection {
	selectKind := func(realm bool) []DiscoverySuggestion {
		grouped := make(map[string][]DiscoveryPost)
		order := make([]string, 0)
		for _, p := range posts {
			id := p.PublisherID
			if realm {
				id = p.RealmID
				if id == "" {
					continue
				}
				if _, ok := in.PublicRealms[id]; !ok {
					continue
				}
			} else if !hasPublisher(p.Post) {
				continue
			}
			if _, ok := grouped[id]; !ok {
				order = append(order, id)
			}
			grouped[id] = append(grouped[id], p)
		}
		candidates := make([]DiscoverySuggestion, 0)
		for _, id := range order {
			group := grouped[id]
			scores := make([]float64, len(group))
			for i, p := range group {
				scores[i] = CalculateDiscoveryPostScore(p, in.Interests, in.Now)
			}
			sort.SliceStable(scores, func(i, j int) bool { return rankGreater(scores[i], scores[j]) })
			score := 0.0
			for _, s := range scores[:min(3, len(scores))] {
				score += s
			}
			if realm {
				score += float64(in.PublicRealms[id].BoostLevel) * 0.6
			} else {
				score += in.Interests.Publishers[id] * 0.4
			}
			if !(score > 0.2) {
				continue
			}
			if realm {
				if in.JoinedRealms[id] || in.HiddenRealms[id] {
					continue
				}
			} else if in.OwnPublishers[id] || in.SubscribedPublishers[id] || in.HiddenPublishers[id] {
				continue
			}
			candidates = append(candidates, DiscoverySuggestion{id, score, discoveryReasons(group)})
		}
		sort.SliceStable(candidates, func(i, j int) bool { return rankGreater(candidates[i].Score, candidates[j].Score) })
		candidates = candidates[:min(8, len(candidates))]
		if realm {
			return candidates
		}
		visible := make([]DiscoverySuggestion, 0, len(candidates))
		for _, c := range candidates {
			publisher, ok := in.Publishers[c.ReferenceID]
			if !ok || (publisher.AccountID != "" && in.HiddenAccounts[publisher.AccountID]) {
				continue
			}
			visible = append(visible, c)
		}
		return visible
	}
	return DiscoverySelection{Publishers: selectKind(false), Realms: selectKind(true)}
}
