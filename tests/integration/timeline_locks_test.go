// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// This wrapper observes the transaction after its real proof/actor locks and
// before publisher locks. It adds no process lock or transaction synchronization.
type timelineLockProbe struct {
	*timelinepg.Repository
	entered chan int
}

var _ postpg.AssociationPrelocker = (*timelineLockProbe)(nil)

func (p *timelineLockProbe) Prelock(ctx context.Context, tx *sql.Tx, actor, publisher string, v posts.Patch, create bool) error {
	var pid int
	if e := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		return e
	}
	select {
	case p.entered <- pid:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.Repository.Prelock(ctx, tx, actor, publisher, v, create)
}

func timelineGuardedWriter(t *testing.T, ctx context.Context, actor string) (*postpg.Repository, context.Context, *timelineLockProbe) {
	t.Helper()
	db := database(t) // Independent pool for each transaction participant.
	if e := authoritypg.New(db).ReplacePolicy(ctx, actor, []authority.Rule{{Pattern: "posts.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	_, verifier, guard, token := interactionAuthority(t, db, actor)
	proof, e := verifier.Authenticate(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	visibility := socialpg.New(db)
	probe := &timelineLockProbe{Repository: timelinepg.New(db).WithVisibility(visibility).WithAssociations(interactionpg.New(db).WithVisibility(visibility)), entered: make(chan int, 1)}
	return postpg.NewWithAuthority(db, guard).WithVisibility(visibility).WithAssociations(probe), guard.Bind(ctx, proof), probe
}

func timelineEntered(t *testing.T, ctx context.Context, p *timelineLockProbe) int {
	t.Helper()
	select {
	case pid := <-p.entered:
		return pid
	case <-ctx.Done():
		t.Fatal("association prelock did not run", ctx.Err())
		return 0
	}
}

func timelinePublisherWait(t *testing.T, ctx context.Context, db *sql.DB, pid int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND position('owned_publishers' in query)>0)`, pid).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected publisher lock wait", ctx.Err())
		case <-ticker.C:
		}
	}
}

func timelinePublisherBlock(t *testing.T, ctx context.Context, db *sql.DB, publisher string) *sql.Tx {
	t.Helper()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, publisher); e != nil {
		t.Fatal(e)
	}
	return tx
}

func TestPGTimelineOppositePublisherRepliesAndRollback(t *testing.T) {
	db := timelineDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, ap := postPublisher(t, db, 3070)
	b, bp := postPublisher(t, db, 3071)
	_, fixturePosts := timelineRepo(db)
	aroot := createPost(t, fixturePosts, a, ap.Name, "opposite root A", posts.Public, false)
	broot := createPost(t, fixturePosts, b, bp.Name, "opposite root B", posts.Public, false)
	aw, ac, aprobe := timelineGuardedWriter(t, ctx, a)
	bw, bc, bprobe := timelineGuardedWriter(t, ctx, b)
	low, high := ap.ID, bp.ID
	if low > high {
		low, high = high, low
	}
	type result struct {
		post posts.Post
		err  error
	}
	runOpposite := func(failA bool) {
		// Both real writes must wait at the smallest publisher. If either takes
		// its larger source first, the independent NOWAIT assertion below fails.
		blocker := timelinePublisherBlock(t, ctx, db, low)
		adone, bdone := make(chan result, 1), make(chan result, 1)
		go func() {
			patch := posts.Patch{Content: postString("opposite reply A"), RepliedPostID: &broot.ID}
			if failA {
				patch.Tags = strList("opposite-rollback-tag")
			}
			p, e := aw.Create(ac, a, ap.Name, patch)
			adone <- result{p, e}
		}()
		go func() {
			p, e := bw.Create(bc, b, bp.ID, posts.Patch{Content: postString("opposite reply B"), RepliedPostID: &aroot.ID})
			bdone <- result{p, e}
		}()
		for _, probe := range []*timelineLockProbe{aprobe, bprobe} {
			timelinePublisherWait(t, ctx, db, timelineEntered(t, ctx, probe))
		}
		check, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		_, e = check.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE NOWAIT`, high)
		_ = check.Rollback()
		if e != nil {
			t.Fatal("opposite writer acquired larger publisher before smallest", e)
		}
		if e = blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		for i, done := range []chan result{adone, bdone} {
			var got result
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal("opposite replies did not complete", ctx.Err())
			}
			if i == 0 && failA {
				var pgerr *pgconn.PgError
				if !errors.As(got.err, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "timeline_opposite_failure" || got.post.ID != "" {
					t.Fatal("canonical audit failure did not roll back", got)
				}
				continue
			}
			parent, publisher := broot.ID, ap.ID
			if i == 1 {
				parent, publisher = aroot.ID, bp.ID
			}
			if got.err != nil || got.post.PublisherID != publisher || got.post.RepliedPostID == nil || *got.post.RepliedPostID != parent {
				t.Fatal("opposite reply failed or lost relation", got)
			}
		}
	}
	runOpposite(false)
	// Fail the final canonical audit after relation/taxonomy writes. NOT VALID
	// deliberately allows already committed synthetic rows for the same actor.
	query(t, db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT timeline_opposite_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000003070'::uuid) NOT VALID`)
	t.Cleanup(func() {
		query(t, db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT IF EXISTS timeline_opposite_failure`)
	})
	runOpposite(true)
	var postCount, relationCount, postAuditCount, timelineAuditCount, tags int
	e := db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM owned_text_posts WHERE publisher_id IN($1::uuid,$2::uuid)),
 (SELECT count(*) FROM owned_post_relations l JOIN owned_text_posts p ON p.id=l.post_id WHERE p.publisher_id IN($1::uuid,$2::uuid)),
 (SELECT count(*) FROM owned_text_post_audit WHERE actor_id IN($3::uuid,$4::uuid)),
 (SELECT count(*) FROM owned_timeline_audit WHERE actor_id IN($3::uuid,$4::uuid)),
 (SELECT count(*) FROM owned_taxonomy.post_tags WHERE slug='opposite-rollback-tag')`, ap.ID, bp.ID, a, b).Scan(&postCount, &relationCount, &postAuditCount, &timelineAuditCount, &tags)
	if e != nil || postCount != 5 || relationCount != 3 || postAuditCount != 5 || timelineAuditCount != 3 || tags != 0 {
		t.Fatal("partial relation/taxonomy/audit state after failed concurrent writer", postCount, relationCount, postAuditCount, timelineAuditCount, tags, e)
	}
}

func TestPGTimelinePrelocksRecheckSourceAndTarget(t *testing.T) {
	db := timelineDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, ap := postPublisher(t, db, 3072)
	b, bp := postPublisher(t, db, 3073)
	_, fixturePosts := timelineRepo(db)
	root := createPost(t, fixturePosts, a, ap.Name, "ancestry retained", posts.Public, false)
	target := createPost(t, fixturePosts, b, bp.Name, "target becomes private", posts.Public, false)
	w, wc, probe := timelineGuardedWriter(t, ctx, a)
	// A selector observed before waiting must still identify the locked source.
	blocker := timelinePublisherBlock(t, ctx, db, ap.ID)
	done := make(chan error, 1)
	go func() {
		_, e := w.Create(wc, a, ap.Name, posts.Patch{Content: postString("stale selector")})
		done <- e
	}()
	timelinePublisherWait(t, ctx, db, timelineEntered(t, ctx, probe))
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_publishers SET name=$2 WHERE id=$1`, ap.ID, ap.Name+"-renamed"); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("stale source selector accepted", e)
	}
	// Canonical APIs never reparent posts; a guarded fixture change while the
	// source lock is pending proves the locked projection is still checked.
	blocker = timelinePublisherBlock(t, ctx, db, ap.ID)
	go func() {
		_, e := w.Update(wc, a, root.ID, posts.Patch{Content: postString("wrong publisher write")})
		done <- e
	}()
	timelinePublisherWait(t, ctx, db, timelineEntered(t, ctx, probe))
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_text_posts SET publisher_id=$2 WHERE id=$1`, root.ID, bp.ID); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, posts.ErrConflict) {
		t.Fatal("source ancestry change accepted", e)
	}
	// Target privacy must be checked after waiting for its publisher lock.
	blocker = timelinePublisherBlock(t, ctx, db, bp.ID)
	go func() {
		_, e := w.Create(wc, a, ap.ID, posts.Patch{Content: postString("hidden target reply"), RepliedPostID: &target.ID})
		done <- e
	}()
	timelinePublisherWait(t, ctx, db, timelineEntered(t, ctx, probe))
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_text_posts SET visibility=$2 WHERE id=$1`, target.ID, posts.Private); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("stale target visibility accepted", e)
	}
	var content string
	var writes, relations int
	e := db.QueryRowContext(ctx, `SELECT content,(SELECT count(*) FROM owned_text_post_audit WHERE actor_id=$2::uuid),(SELECT count(*) FROM owned_post_relations l JOIN owned_text_posts p ON p.id=l.post_id WHERE p.publisher_id IN($3::uuid,$4::uuid)) FROM owned_text_posts WHERE id=$1::uuid`, root.ID, a, ap.ID, bp.ID).Scan(&content, &writes, &relations)
	if e != nil || content != "ancestry retained" || writes != 1 || relations != 0 {
		t.Fatal("failed prelock recheck left mutation state", content, writes, relations, e)
	}
}

func TestPGTimelinePublisherUUIDSelectorCollision(t *testing.T) {
	db := timelineDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	actor, original := postPublisher(t, db, 3074)
	repo := accountpg.NewForCompatibilityTests(db)
	collision, e := repo.CreatePublisher(ctx, actor, accounts.PublisherPatch{Name: postString("timeline-selector-collision-3074"), Nick: postString("Synthetic UUID-shaped publisher name")})
	if e != nil {
		t.Fatal(e)
	}
	// Current creation validation forbids UUID-shaped names; an isolated SQL
	// fixture exercises preexisting/imported rows without weakening that rule.
	query(t, db, `UPDATE owned_publishers SET name=$2 WHERE id=$1`, collision.ID, original.ID)
	for _, selector := range []string{original.ID, original.Name} {
		got, e := repo.LookupPublisher(ctx, selector)
		if e != nil || got.ID != original.ID {
			t.Fatal("UUID/name source resolution disagrees", selector, got.ID, e)
		}
	}
	w, wc, probe := timelineGuardedWriter(t, ctx, actor)
	if _, e = w.Create(wc, actor, "", posts.Patch{Content: postString("ambiguous default")}); !errors.Is(e, accounts.ErrAmbiguous) {
		t.Fatal("missing default should retain canonical ambiguity rule", e)
	}
	query(t, db, `INSERT INTO owned_publishing_settings(account_id,default_publisher_id) VALUES($1,$2)`, actor, original.ID)
	for _, selector := range []string{original.ID, original.Name, collision.ID, ""} {
		p, e := w.Create(wc, actor, selector, posts.Patch{Content: postString("deterministic publisher selector")})
		if e != nil {
			t.Fatal("guarded deterministic selector write", selector, e)
		}
		_ = timelineEntered(t, ctx, probe)
		expected := original.ID
		if selector == collision.ID {
			expected = collision.ID
		}
		if e != nil || p.PublisherID != expected {
			t.Fatal("prelock and editor lookup resolved different publishers", selector, p.PublisherID, e)
		}
	}
	// No UUID-shaped name fallback is allowed when the canonical ID is absent.
	absent := authorityUUID(t)
	query(t, db, `UPDATE owned_publishers SET name=$2 WHERE id=$1`, collision.ID, absent)
	if _, e = repo.LookupPublisher(ctx, absent); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("UUID-shaped name shadowed an absent canonical ID", e)
	}
	if _, e = w.Create(wc, actor, absent, posts.Patch{Content: postString("ambiguous UUID-shaped name")}); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("prelock accepted UUID-shaped name fallback", e)
	}
}
