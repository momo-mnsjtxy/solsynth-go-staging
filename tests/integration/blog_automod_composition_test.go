// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/sphere"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/reader"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"testing"
	"time"
)

func TestPGBlogAutomodUsesPreparedMetadataAndSafeProjection(t *testing.T) {
	admin := automodFixture(t, false)
	marker := "synthetic-server-title-" + authorityUUID(t)
	rule := automodRule(t, admin, a.Hide, marker)
	f := newBlogFixture(t)
	f.configBlog.Previewer = blogPreviewFunc(func(_ context.Context, u string) (reader.Link, error) { return reader.Link{URL: u, Title: marker}, nil })
	f = blogPool(t, f)
	f.posts.WithAutomod(false)
	verifiedBlogDomain(t, f)
	// Empty authored title receives the verified preview fallback before automod.
	p := createBlog(t, f, posts.Patch{})
	if p.Title == nil || *p.Title != marker || p.Automod == nil || !p.Automod.Hidden || len(p.Automod.Matches) != 0 || p.Automod.Review != nil {
		t.Fatal("automod did not evaluate server metadata with safe response", p)
	}
	var matched string
	if e := f.db.QueryRow(`SELECT automod_decision->'matches'->0->>'rule_id' FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&matched); e != nil || matched != rule.ID {
		t.Fatal("canonical global decision missing", matched, e)
	}
	feed := timelinepg.NewWithAuthority(f.db, f.guard).WithVisibility(socialpg.New(f.db)).WithAssociations(f.links).WithAutomod(false)
	for _, mode := range []sphere.Mode{sphere.Top, sphere.Latest} {
		page, e := feed.Feed(f.ctx, f.actor, timeline.Options{Publisher: f.pub.Name, Mode: mode, Take: 20})
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, event := range page.Items {
			if event.Data != nil && event.Data.ID == p.ID {
				found = true
			}
		}
		if found != (mode == sphere.Latest) {
			t.Fatal("ranked/default latest contract", mode, found)
		}
	}
	future := time.Now().UTC().Add(time.Hour)
	scheduled := createBlog(t, f, posts.Patch{PublishedAt: &future})
	if scheduled.Schedule == nil {
		t.Fatal("missing schedule")
	}
	scheduleDue(t, f.db, scheduled)
	if _, e := f.posts.ProcessScheduled(f.ctx, 20); e != nil {
		t.Fatal(e)
	}
	current, e := f.posts.Get(f.ctx, f.actor, scheduled.ID)
	if e != nil || current.DraftedAt != nil || current.Automod == nil || !current.Automod.Hidden || len(current.Automod.Matches) != 0 {
		t.Fatal("scheduled blog/automod composition", current, e)
	}
}
