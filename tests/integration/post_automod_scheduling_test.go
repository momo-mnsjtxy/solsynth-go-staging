// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/sphere"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
	"time"
)

func TestPGPostAutomodScheduledPublicationCurrentRulesAndReplay(t *testing.T) {
	f := automodFixture(t, false)
	query(t, f.db, postpg.SchedulingSchema)
	f.posts.WithScheduling()
	body := "scheduled-auto-" + authorityUUID(t)
	due := scheduleFuture(t, f.db)
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: &body, PublishedAt: &due})
	if e != nil || p.Schedule == nil || p.Automod == nil || p.Automod.Hidden {
		t.Fatal(p, e)
	}
	automodRule(t, f, a.Hide, body)
	scheduleDue(t, f.db, p)
	if n, e := f.posts.ProcessScheduled(context.Background(), 100); e != nil || n < 1 {
		t.Fatal(n, e)
	}
	scheduleState(t, f.db, p.Schedule.ID, "published")
	p, e = f.posts.Get(f.ctx, f.actor, p.ID)
	if e != nil || p.Automod == nil || !p.Automod.Hidden || p.Automod.Stale || p.DraftedAt != nil {
		t.Fatal(p, e)
	}
	if automodHas(automodFeed(t, f, sphere.Top), p.ID) || !automodHas(automodFeed(t, f, sphere.Latest), p.ID) {
		t.Fatal("source Hide changed scheduled publication semantics")
	}
	var before int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_post_automod_events WHERE post_id=$1`, p.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.ProcessScheduled(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	var after int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_post_automod_events WHERE post_id=$1`, p.ID).Scan(&after); e != nil || after != before {
		t.Fatal("retry duplicated decisions", before, after, e)
	}
}
func TestPGPostAutomodScheduledGenerationWaitRechecksSession(t *testing.T) {
	f := automodFixture(t, false)
	query(t, f.db, postpg.SchedulingSchema)
	f.posts.WithScheduling()
	body := "scheduled-expiry-auto-" + authorityUUID(t)
	due := scheduleFuture(t, f.db)
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: &body, PublishedAt: &due})
	if e != nil || p.Schedule == nil {
		t.Fatal(p, e)
	}
	scheduleDue(t, f.db, p)
	var expires time.Time
	if e = f.db.QueryRow(`UPDATE owned_authority_sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=(SELECT session_id FROM owned_post_schedules WHERE id=$1) RETURNING expires_at`, p.Schedule.ID).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var blocker int
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT generation FROM owned_automod_state WHERE singleton FOR UPDATE`); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.posts.ProcessScheduled(ctx, 100); done <- e }()
	moderationWait(t, ctx, f.db, blocker, "owned_automod_state")
	var active bool
	if e = f.db.QueryRowContext(ctx, `SELECT clock_timestamp()<$1`, expires).Scan(&active); e != nil || !active {
		t.Fatal("session expired before generation wait", e)
	}
	waitAutomodClock(t, ctx, f.db, expires)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	scheduleState(t, f.db, p.Schedule.ID, "blocked")
	var failure string
	var published bool
	if e = f.db.QueryRow(`SELECT s.failure,p.published_at IS NOT NULL FROM owned_post_schedules s JOIN owned_text_posts p ON p.id=s.post_id WHERE s.id=$1`, p.Schedule.ID).Scan(&failure, &published); e != nil || failure != "authority_revoked" || published {
		t.Fatal(failure, published, e)
	}
}
