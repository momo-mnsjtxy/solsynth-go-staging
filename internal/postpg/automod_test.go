// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
	"time"
)

func TestAutomodDismissalBoundToCurrentContentRulesAndMode(t *testing.T) {
	now := time.Now().UTC()
	s := AutomodSnapshot{Generation: 7, At: now, Rules: []a.Rule{{ID: "synthetic", Name: "synthetic", Type: a.KeywordBlocklist, DefaultAction: a.Hide, Pattern: "token", IsEnabled: true}}}
	p := posts.Post{Content: "token"}
	d, e := ComputeAutomod(context.Background(), s, p, nil, false)
	if e != nil || !d.Hidden {
		t.Fatal(d, e)
	}
	d.Review = &posts.AutomodReviewRecord{ActorID: "synthetic moderator", Reason: "synthetic"}
	for _, tt := range []struct {
		name   string
		s      AutomodSnapshot
		p      posts.Post
		ignore bool
		hidden bool
	}{
		{"same", s, p, false, false},
		{"disabled owned review", s, p, true, true},
		{"generation", AutomodSnapshot{Generation: 8, At: now, Rules: s.Rules}, p, false, true},
		{"content", s, posts.Post{Content: "new token"}, false, true},
	} {
		got, e := ComputeAutomod(context.Background(), tt.s, tt.p, d, tt.ignore)
		if e != nil || got.Hidden != tt.hidden {
			t.Fatal(tt.name, got, e)
		}
	}
	s.Rules[0].Pattern = "new"
	got, e := ComputeAutomod(context.Background(), s, posts.Post{Content: "new token"}, d, false)
	if e != nil || got.Review != nil || !got.Hidden {
		t.Fatal("changed rule without generation still invalidates review", got, e)
	}
}
