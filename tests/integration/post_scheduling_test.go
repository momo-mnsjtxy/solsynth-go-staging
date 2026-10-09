// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func newPostSchedulingFixture(t *testing.T) postMediaFixture {
	t.Helper()
	f := newPostMediaFixture(t)
	query(t, f.db, postpg.SchedulingSchema)
	f.config.Now = time.Now
	return postSchedulingAuthority(t, f)
}
func postSchedulingAuthority(t *testing.T, f postMediaFixture) postMediaFixture {
	t.Helper()
	var e error
	f.issuer, e = authority.New(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	return postSchedulingPool(t, f, f.db)
}
func postSchedulingPool(t *testing.T, f postMediaFixture, db *sql.DB) postMediaFixture {
	t.Helper()
	f = postMediaPool(t, f, db)
	timeline := timelinepg.New(db).WithAssociations(f.links).WithVisibility(socialpg.New(db))
	f.posts.WithAssociations(timeline).WithScheduling()
	if e := f.posts.SchedulingReady(context.Background()); e != nil {
		t.Fatal(e)
	}
	return f
}
func scheduleFuture(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var at time.Time
	if e := db.QueryRow(`SELECT clock_timestamp()+interval '1 hour'`).Scan(&at); e != nil {
		t.Fatal(e)
	}
	return at
}
func scheduledPost(t *testing.T, f postMediaFixture, patch posts.Patch) posts.Post {
	t.Helper()
	due := scheduleFuture(t, f.db)
	patch.PublishedAt = &due
	if patch.Content == nil {
		patch.Content = postString("Synthetic scheduled post")
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, patch)
	if e != nil || p.Schedule == nil || p.Schedule.State != "pending" || p.DraftedAt == nil || p.PublishedAt != nil {
		t.Fatal(p, e)
	}
	return p
}
func scheduleDue(t *testing.T, db *sql.DB, p posts.Post) {
	t.Helper()
	query(t, db, `UPDATE owned_post_schedules SET due_at=clock_timestamp() WHERE id=$1`, p.Schedule.ID)
}
func scheduleState(t *testing.T, db *sql.DB, id int64, want string) {
	t.Helper()
	var state string
	if e := db.QueryRow(`SELECT state FROM owned_post_schedules WHERE id=$1`, id).Scan(&state); e != nil || state != want {
		t.Fatal(state, want, e)
	}
}
func scheduleHidden(t *testing.T, f postMediaFixture, id string) {
	t.Helper()
	if _, e := f.posts.Get(context.Background(), "", id); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("uncommitted scheduled publication leaked", e)
	}
}
func processSchedule(t *testing.T, f postMediaFixture) {
	t.Helper()
	if n, e := f.posts.ProcessScheduled(context.Background(), 100); e != nil || n < 1 {
		t.Fatal(n, e)
	}
}
func TestPGPostScheduleLifecycleAndNoEarlyVisibility(t *testing.T) {
	f := newPostSchedulingFixture(t)
	file := privatePostFile(t, f, "scheduled", "scheduled private bytes")
	p := scheduledPost(t, f, posts.Patch{Attachments: mediaIDs(file.ID)})
	scheduleHidden(t, f, p.ID)
	missingPostFile(t, f, context.Background(), "", file.ID)
	if n, e := f.posts.ProcessScheduled(context.Background(), 100); e != nil || n != 0 {
		t.Fatal("published before due", n, e)
	}
	wire, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	var dto posts.Post
	if e = json.Unmarshal(wire, &dto); e != nil || dto.PublishedAt == nil || dto.DraftedAt != nil {
		t.Fatal(string(wire), e)
	}
	changed := "Edited scheduled body"
	p, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Content: &changed})
	if e != nil || p.Schedule == nil || p.Schedule.State != "pending" {
		t.Fatal(p, e)
	}
	scheduleDue(t, f.db, p)
	scheduleHidden(t, f, p.ID)
	missingPostFile(t, f, context.Background(), "", file.ID)
	processSchedule(t, f)
	scheduleState(t, f.db, p.Schedule.ID, "published")
	visible, e := f.posts.Get(context.Background(), "", p.ID)
	if e != nil || visible.Content != changed || visible.DraftedAt != nil || visible.PublishedAt == nil {
		t.Fatal(visible, e)
	}
	mediaRead(t, f.repo, context.Background(), "", file.ID, "scheduled private bytes")
	if n, e := f.posts.ProcessScheduled(context.Background(), 100); e != nil || n != 0 {
		t.Fatal("publication replay", n, e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_post_schedule_events WHERE schedule_id=$1 AND kind='published'`, p.Schedule.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	private := posts.Private
	hidden := scheduledPost(t, f, posts.Patch{Visibility: &private})
	scheduleDue(t, f.db, hidden)
	processSchedule(t, f)
	scheduleHidden(t, f, hidden.ID)
}
func TestPGPostScheduleCancellationReplacementAndDelete(t *testing.T) {
	f := newPostSchedulingFixture(t)
	p := scheduledPost(t, f, posts.Patch{})
	original := p.Schedule.ID
	future := scheduleFuture(t, f.db).Add(time.Hour)
	p, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{PublishedAt: &future})
	if e != nil || p.Schedule.ID == original {
		t.Fatal(p, e)
	}
	scheduleState(t, f.db, original, "cancelled")
	at := time.Now().UTC()
	cancelled, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{DraftedAt: &at})
	if e != nil || cancelled.PublishedAt != nil || cancelled.DraftedAt == nil {
		t.Fatal(cancelled, e)
	}
	scheduleState(t, f.db, p.Schedule.ID, "cancelled")
	scheduleDue(t, f.db, p)
	if n, e := f.posts.ProcessScheduled(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	scheduleHidden(t, f, p.ID)
	deleted := scheduledPost(t, f, posts.Patch{})
	if e = f.posts.Delete(f.ctx, f.actor, deleted.ID); e != nil {
		t.Fatal(e)
	}
	scheduleState(t, f.db, deleted.Schedule.ID, "cancelled")
	scheduleDue(t, f.db, deleted)
	if n, e := f.posts.ProcessScheduled(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	manual := scheduledPost(t, f, posts.Patch{})
	published, e := f.posts.Publish(f.ctx, f.actor, manual.ID)
	if e != nil || published.PublishedAt == nil {
		t.Fatal(published, e)
	}
	scheduleState(t, f.db, manual.Schedule.ID, "cancelled")
}
func TestPGPostScheduleAuthorityRefreshLogoutAndPolicyRevocation(t *testing.T) {
	for _, kind := range []string{"expired_access", "refresh", "logout", "session_expired", "permission", "disable", "membership"} {
		t.Run(kind, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			if kind == "expired_access" {
				f.config.AccessTTL = 5 * time.Second
				f = postSchedulingAuthority(t, f)
			}
			pair := pgIssue(t, f.issuer, f.actor)
			f.token = pair.Token
			f.ctx = mediaProof(t, f.mediaFixture, pair.Token)
			p := scheduledPost(t, f, posts.Patch{})
			proof, e := f.verifier.Authenticate(f.ctx, pair.Token)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "expired_access":
				// JWT exp is whole seconds by contract. Wait for this token's
				// real deadline without advancing any application/database clock.
				deadline := time.Unix(proof.Session.CreatedAt.Add(f.config.AccessTTL).Unix(), 0)
				waitPostScheduleClock(t, f.db, deadline)
				if _, e = f.verifier.Authenticate(context.Background(), pair.Token); !errors.Is(e, authority.ErrExpired) {
					t.Fatal("fixture access token did not expire", e)
				}
			case "refresh":
				if _, e = f.issuer.Refresh(context.Background(), pair.RefreshToken, 0, ""); e != nil {
					t.Fatal(e)
				}
			case "logout":
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp(),authorization_epoch=authorization_epoch+1,epoch=epoch+1 WHERE id=$1`, proof.Session.ID)
			case "session_expired":
				query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp() WHERE id=$1`, proof.Session.ID)
			case "permission":
				if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "media.files.*"}}, nil); e != nil {
					t.Fatal(e)
				}
			case "disable":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "membership":
				query(t, f.db, `UPDATE owned_publisher_members SET revoked_at=clock_timestamp(),updated_at=clock_timestamp() WHERE publisher_id=$1 AND account_id=$2`, f.pub.ID, f.actor)
			}
			scheduleDue(t, f.db, p)
			processSchedule(t, f)
			want := "blocked"
			if kind == "expired_access" || kind == "refresh" {
				want = "published"
			}
			scheduleState(t, f.db, p.Schedule.ID, want)
			if want == "blocked" {
				scheduleHidden(t, f, p.ID)
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
				if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
					t.Fatal(e)
				}
				if n, e := f.posts.ProcessScheduled(context.Background(), 100); e != nil || n != 0 {
					t.Fatal("revoked schedule revived", n, e)
				}
			}
		})
	}
}
func TestPGPostScheduleRollbackConcurrencyAndBoundary(t *testing.T) {
	f := newPostSchedulingFixture(t)
	p := scheduledPost(t, f, posts.Patch{})
	scheduleDue(t, f.db, p)
	constraint := "schedule_fail_" + strings.ReplaceAll(p.ID, "-", "")
	query(t, f.db, `ALTER TABLE owned_post_schedule_events ADD CONSTRAINT `+constraint+` CHECK(schedule_id<>`+fmt.Sprint(p.Schedule.ID)+` OR kind<>'published')`)
	if _, e := f.posts.ProcessScheduled(context.Background(), 100); e == nil {
		t.Fatal("publication event failure succeeded")
	}
	query(t, f.db, `ALTER TABLE owned_post_schedule_events DROP CONSTRAINT `+constraint)
	scheduleHidden(t, f, p.ID)
	scheduleState(t, f.db, p.Schedule.ID, "pending")
	var wg sync.WaitGroup
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.posts.ProcessScheduled(context.Background(), 100); done <- e }()
	}
	wg.Wait()
	close(done)
	for e := range done {
		if e != nil {
			t.Fatal(e)
		}
	}
	scheduleState(t, f.db, p.Schedule.ID, "published")
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1 AND action='publish'`, p.ID).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestPGPostScheduleWaitObservesCancellationAndUnavailableMedia(t *testing.T) {
	for _, kind := range []string{"cancel", "edit", "delete", "media", "revoke"} {
		t.Run(kind, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			file := privatePostFile(t, f, "wait", "scheduled wait bytes")
			p := scheduledPost(t, f, posts.Patch{Attachments: mediaIDs(file.ID)})
			scheduleDue(t, f.db, p)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "owned_authority_accounts", f.actor
			if kind == "media" {
				table, id = "owned_media_files", file.ID
			}
			blocker, pid := atomicBlocker(t, locks, table, id)
			var e error
			switch kind {
			case "cancel":
				_, e = blocker.Exec(`UPDATE owned_post_schedules SET state='cancelled',revision=revision+1,finished_at=clock_timestamp() WHERE id=$1`, p.Schedule.ID)
			case "edit":
				_, e = blocker.Exec(`UPDATE owned_post_schedules SET revision=revision+1,due_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, p.Schedule.ID)
			case "delete":
				_, e = blocker.Exec(`UPDATE owned_text_posts SET deleted_at=clock_timestamp() WHERE id=$1`, p.ID)
			case "media":
				_, e = blocker.Exec(`UPDATE owned_media_files SET state='deleting' WHERE id=$1`, file.ID)
			case "revoke":
				_, e = blocker.Exec(`DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := f.posts.ProcessScheduled(ctx, 100); done <- e }()
			waitAtomicBlocked(t, locks, pid, table)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			want := "blocked"
			if kind == "cancel" {
				want = "cancelled"
			}
			if kind == "edit" {
				want = "pending"
			}
			scheduleState(t, f.db, p.Schedule.ID, want)
			scheduleHidden(t, f, p.ID)
		})
	}
}
func TestPGPostScheduleRealmModerationAndRelatedVisibility(t *testing.T) {
	f := newRealmPublishingFixture(t)
	query(t, f.db, postpg.SchedulingSchema)
	f.posts.WithScheduling()
	publisher := postRealmOtherPublisher(t, f)
	due := scheduleFuture(t, f.db)
	p, e := f.posts.Create(f.otherCtx, f.other, publisher.ID, posts.Patch{Content: postString("Realm scheduled"), PublishedAt: &due, RealmID: &f.private.ID})
	if e != nil {
		t.Fatal(e)
	}
	deny := false
	if _, e = f.social.SetUserPermission(f.ctx, f.actor, f.private.ID, social.UserPermissionRequest{AccountID: f.other, PermissionOverrides: social.PermissionOverrides{CanPost: &deny}}); e != nil {
		t.Fatal(e)
	}
	scheduleDue(t, f.db, p)
	if _, e = f.posts.ProcessScheduled(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	scheduleState(t, f.db, p.Schedule.ID, "blocked")

	if _, e = f.social.SetUserPermission(f.ctx, f.actor, f.private.ID, social.UserPermissionRequest{AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	due = scheduleFuture(t, f.db)
	waiting, e := f.posts.Create(f.otherCtx, f.other, publisher.ID, posts.Patch{Content: postString("Realm wait"), PublishedAt: &due, RealmID: &f.private.ID})
	if e != nil {
		t.Fatal(e)
	}
	scheduleDue(t, f.db, waiting)
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: waitCtx}
	blocker, pid := atomicBlocker(t, locks, "owned_realms", f.private.ID)
	if _, e = blocker.Exec(`UPDATE owned_realm_role_permissions SET permission_mask=permission_mask & (~2),updated_at=clock_timestamp() WHERE realm_id=$1 AND role_level=0`, f.private.ID); e != nil {
		t.Fatal(e)
	}
	waited := make(chan error, 1)
	go func() { _, e := f.posts.ProcessScheduled(waitCtx, 100); waited <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_realms")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-waited; e != nil {
		t.Fatal(e)
	}
	scheduleState(t, f.db, waiting.Schedule.ID, "blocked")
	// Moderation can invalidate a previously scheduled operation without rewriting
	// its draft. Even a later unlock cannot restart this consumed intent.
	plain := newPostSchedulingFixture(t)
	mod := scheduledPost(t, plain, posts.Patch{})
	query(t, plain.db, `UPDATE owned_text_posts SET locked_at=clock_timestamp() WHERE id=$1`, mod.ID)
	scheduleDue(t, plain.db, mod)
	processSchedule(t, plain)
	scheduleState(t, plain.db, mod.Schedule.ID, "blocked")
	query(t, plain.db, `UPDATE owned_text_posts SET locked_at=NULL WHERE id=$1`, mod.ID)
	if n, e := plain.posts.ProcessScheduled(context.Background(), 100); e != nil || n != 0 {
		t.Fatal("unlock revived schedule", n, e)
	}
	other := newPostSchedulingFixture(t)
	parent, e := other.posts.Create(other.ctx, other.actor, other.pub.ID, posts.Patch{Content: postString("Public parent")})
	if e != nil {
		t.Fatal(e)
	}
	child := scheduledPost(t, plain, posts.Patch{RepliedPostID: &parent.ID})
	private := posts.Private
	if _, e = other.posts.Update(other.ctx, other.actor, parent.ID, posts.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	scheduleDue(t, plain.db, child)
	processSchedule(t, plain)
	scheduleState(t, plain.db, child.Schedule.ID, "blocked")
	scheduleHidden(t, plain, child.ID)
}
func TestPGPostScheduleBackendProjectionAndDeferredFederation(t *testing.T) {
	f := newPostSchedulingFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 65536})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	due := scheduleFuture(t, f.db)
	p := backendDecode[posts.Post](t, backendCall(t, app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, fmt.Sprintf(`{"content":"Scheduled HTTP body","published_at":%q}`, due.Format(time.RFC3339Nano)), 200))
	if p.PublishedAt == nil || p.DraftedAt != nil || p.Schedule == nil || p.Schedule.State != "pending" {
		t.Fatal(p)
	}
	backendCall(t, app, "GET", "/sphere/posts/"+p.ID, "", "", 404)
	scheduleDue(t, f.db, p)
	backendCall(t, app, "GET", "/sphere/posts/"+p.ID, "", "", 404)
	if n, e := app.ProcessScheduledPosts(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	backendCall(t, app, "GET", "/sphere/posts/"+p.ID, "", "", 200)
	// Existing federation projection sees only actual committed publication.
	x := newFederationFixture(t, true)
	query(t, x.db, postpg.SchedulingSchema)
	repo := postpg.NewWithAuthority(x.db, x.guard).WithVisibility(socialpg.New(x.db)).WithAssociations(timelinepg.New(x.db)).WithScheduling()
	at := scheduleFuture(t, x.db)
	post, e := repo.Create(x.ctx, x.actor, x.publisher, posts.Patch{Content: postString("Deferred federation"), PublishedAt: &at})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = x.r.ReconcilePosts(x.ctx, 100); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_post_state WHERE post_id=$1`, post.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("early federation state", n, e)
	}
	query(t, x.db, `UPDATE owned_post_schedules SET due_at=$2 WHERE id=$1`, post.Schedule.ID, x.config.Clock().Add(-time.Second))
	if _, e = x.r.ReconcilePosts(x.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_post_state WHERE post_id=$1`, post.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("clock alone federated", n, e)
	}
	if _, e = repo.ProcessScheduled(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	if _, e = x.r.ReconcilePosts(x.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_post_state WHERE post_id=$1`, post.ID).Scan(&n); e != nil || n != 1 {
		t.Fatal("committed publication missing", n, e)
	}
}

type postScheduleSaved struct {
	Post, Pending, Blocked, Actor, File, Objects, Snapshot string
	Due                                                    time.Time
}

func postScheduleSnapshot(t *testing.T, db *sql.DB, actor string) string {
	t.Helper()
	var snapshot string
	e := db.QueryRow(`SELECT jsonb_build_object('posts',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE u.account_id=$1),'schedules',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM owned_post_schedules s WHERE s.actor_id=$1),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM owned_post_schedule_events e WHERE e.actor_id=$1))::text`, actor).Scan(&snapshot)
	if e != nil {
		t.Fatal(e)
	}
	return snapshot
}
func TestPGPostScheduleSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	if phase == "before" {
		f := newPostSchedulingFixture(t)
		objects := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "post-schedule-objects")
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.root = objects
		f = postSchedulingPool(t, f, db)
		file := privatePostFile(t, f, "persist", "scheduled persisted bytes")
		p := scheduledPost(t, f, posts.Patch{Attachments: mediaIDs(file.ID)})
		scheduleDue(t, db, p)
		processSchedule(t, f)
		pending := scheduledPost(t, f, posts.Patch{})
		query(t, db, `UPDATE owned_post_schedules SET due_at=clock_timestamp()+interval '30 days' WHERE id=$1`, pending.Schedule.ID)
		blocked := scheduledPost(t, f, posts.Patch{})
		query(t, db, `UPDATE owned_text_posts SET locked_at=clock_timestamp() WHERE id=$1`, blocked.ID)
		scheduleDue(t, db, blocked)
		processSchedule(t, f)
		var due time.Time
		if e := db.QueryRow(`SELECT published_at FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&due); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, "post-scheduling.json", postScheduleSaved{p.ID, pending.ID, blocked.ID, f.actor, file.ID, objects, postScheduleSnapshot(t, db, f.actor), due})
		return
	}
	var state postScheduleSaved
	loadRestartFixture(t, "post-scheduling.json", &state)
	if snapshot := postScheduleSnapshot(t, db, state.Actor); snapshot != state.Snapshot {
		t.Fatal("saved publication/schedule/events changed across restart")
	}
	repo := postpg.New(db).WithVisibility(socialpg.New(db)).WithScheduling()
	visible, e := repo.Get(context.Background(), "", state.Post)
	if e != nil || visible.PublishedAt == nil || !visible.PublishedAt.Equal(state.Due) {
		t.Fatal(visible, e)
	}
	for _, id := range []string{state.Pending, state.Blocked} {
		if _, e = repo.Get(context.Background(), "", id); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("unpublished fixture exposed", id, e)
		}
	}
	store, e := mediastore.NewFilesystem(state.Objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	mediaRepo, e := mediapg.New(db, store, authoritypg.NewTransactionGuard(authoritypg.New(db), nil), mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	// Reuse the canonical current post audience for the bytes persisted before restart.
	mediaRepo.WithReadVisibility(postmediapg.New(db, nil))
	mediaRead(t, mediaRepo, context.Background(), "", state.File, "scheduled persisted bytes")
}
