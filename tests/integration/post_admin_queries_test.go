// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
	"time"
)

func TestPGPostAdminDescriptionSearchAndPublishedOrdering(t *testing.T) {
	f := newModerationFixture(t)
	older := f.post(t, f.individual, "Synthetic older post", posts.Private)
	newer := f.post(t, f.individual, "Synthetic newer post", posts.Public)
	now := f.config.Now()
	query(t, f.db, `UPDATE owned_text_posts SET description='Distinctive administrative description',created_at=$2,published_at=$3 WHERE id=$1`, older.ID, now.Add(-48*time.Hour), now)
	query(t, f.db, `UPDATE owned_text_posts SET created_at=$2,published_at=$3 WHERE id=$1`, newer.ID, now.Add(-time.Hour), now.Add(-24*time.Hour))
	out, total, e := f.admin.List(f.outsideCtx, f.outside, c.PostKind, c.Query{Take: 10, PublisherID: f.individual.ID})
	if e != nil || total != 2 {
		t.Fatal(out, total, e)
	}
	rows := out.([]any)
	if rows[0].(c.Post).ID != older.ID {
		t.Fatal("list did not prefer publication date", rows)
	}
	out, total, e = f.admin.List(f.outsideCtx, f.outside, c.PostKind, c.Query{Take: 10, PublisherID: f.individual.ID, Text: "  ADMINISTRATIVE  "})
	if e != nil || total != 1 || out.([]any)[0].(c.Post).ID != older.ID {
		t.Fatal(out, total, e)
	}
	out, total, e = f.admin.List(f.outsideCtx, f.outside, c.PostKind, c.Query{Take: 10, PublisherID: f.individual.ID, Text: "Distinctive%description"})
	if e != nil || total != 1 {
		t.Fatal("source ILIKE wildcard semantics", out, total, e)
	}
}
