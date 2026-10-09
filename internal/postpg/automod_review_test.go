// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
	"time"
)

func TestAutomodReviewInvalidationCannotReviveOnContentABA(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	s := AutomodSnapshot{Generation: 7, At: now, Rules: []a.Rule{{ID: "synthetic-original", Name: "Synthetic rule", Type: a.KeywordBlocklist, DefaultAction: a.Hide, Pattern: "token", IsEnabled: true}}}
	original := posts.Post{Content: "token"}
	prior, e := ComputeAutomod(context.Background(), s, original, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	prior.Review = &posts.AutomodReviewRecord{ActorID: "synthetic moderator", Reason: "Synthetic review"}
	changed, e := ComputeAutomod(context.Background(), s, posts.Post{Content: "different token"}, prior, false)
	if e != nil || changed.Review != nil || !changed.Hidden {
		t.Fatal(changed, e)
	}
	restored, e := ComputeAutomod(context.Background(), s, original, changed, false)
	if e != nil || restored.Review != nil || !restored.Hidden {
		t.Fatal("returning to the old body revived a discarded dismissal", restored, e)
	}
	// Moving the same joined text to another field is a new exact input too.
	title := original.Content
	moved, e := ComputeAutomod(context.Background(), s, posts.Post{Title: &title}, prior, false)
	if e != nil || moved.Review != nil || !moved.Hidden {
		t.Fatal("field-boundary change reused an old dismissal", moved, e)
	}
}

func TestAutomodReviewInvalidatesOnExpiryAndIdenticalRuleReplacement(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	s := AutomodSnapshot{Generation: 7, At: now, Rules: []a.Rule{
		{ID: "synthetic-hide", Name: "Synthetic rule", Type: a.KeywordBlocklist, DefaultAction: a.Hide, Pattern: "token", IsEnabled: true},
		{ID: "synthetic-expiry", Name: "Synthetic expiring rule", Type: a.KeywordBlocklist, DefaultAction: a.Flag, Pattern: "token", IsEnabled: true, ExpiresAt: &expires},
	}}
	p := posts.Post{Content: "token"}
	prior, e := ComputeAutomod(context.Background(), s, p, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	prior.Review = &posts.AutomodReviewRecord{ActorID: "synthetic moderator", Reason: "Synthetic review"}
	s.At = expires
	current, e := ComputeAutomod(context.Background(), s, p, prior, false)
	if e != nil || current.Review != nil || !current.Hidden || current.Flagged {
		t.Fatal("expiry without generation change retained dismissal", current, e)
	}
	s.At = now
	s.Rules[0].ID = "synthetic-replacement"
	current, e = ComputeAutomod(context.Background(), s, p, prior, false)
	if e != nil || current.Review != nil || !current.Hidden {
		t.Fatal("identical rule with different identity retained dismissal", current, e)
	}
	s.Rules[0].ID = "synthetic-hide"
	s.Generation++
	current, e = ComputeAutomod(context.Background(), s, p, prior, false)
	if e != nil || current.Review != nil || !current.Hidden {
		t.Fatal("generation-changing rule ABA retained dismissal", current, e)
	}
}
