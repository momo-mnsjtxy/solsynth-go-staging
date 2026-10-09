// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type collectionPublicationFixture struct {
	collectionNavigationFixture
	appID string
}

func newCollectionPublicationFixture(t *testing.T) collectionPublicationFixture {
	t.Helper()
	f := collectionPublicationFixture{collectionNavigationFixture: newCollectionNavigationFixture(t)}
	f.appID = "collection.publication." + f.actor
	f.app = surveySubscriptionBackend(t, f.db, f.config, f.appID)
	backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 201)
	return f
}
func publicationCreate(t *testing.T, f collectionPublicationFixture, values map[string]any) posts.Post {
	t.Helper()
	if values == nil {
		values = map[string]any{}
	}
	if _, ok := values["content"]; !ok {
		values["content"] = "private-body-sentinel"
	}
	values["collection_ids"] = []string{f.collection.ID}
	return backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, values, 200))
}
func publicationDrain(t *testing.T, f collectionPublicationFixture) {
	t.Helper()
	for {
		n, e := f.app.ProcessCollectionPublicationNotifications(context.Background(), 100)
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			return
		}
	}
}
func publicationInboxCount(t *testing.T, f collectionPublicationFixture) int {
	t.Helper()
	return subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND app_id=$2 AND topic='posts.new'`, f.other, f.appID)
}
func publicationReadRepo(t *testing.T, f collectionPublicationFixture) (*notificationpg.Repository, context.Context) {
	t.Helper()
	v, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(authoritypg.New(f.db), v)
	p, e := v.Authenticate(context.Background(), f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	policy := interactionpg.New(f.db).WithVisibility(socialpg.New(f.db)).WithPublicationNotifications(f.appID)
	return notificationpg.New(f.db, g, f.appID).WithCollectionPublications(policy), g.Bind(context.Background(), p)
}
func TestPGCollectionPublicationLifecycleAndDeduplication(t *testing.T) {
	f := newCollectionPublicationFixture(t)
	second := backendDecode[posts.Collection](t, backendCall(t, f.app, "POST", "/sphere/publishers/"+f.pub.Name+"/collections", f.token, `{"slug":"second"}`, 201))
	backendCall(t, f.app, "POST", "/sphere/publishers/"+f.pub.Name+"/collections/second/subscribe", f.otherToken, "", 201)
	p := backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": strings.Repeat("secret-body", 400), "collection_ids": []string{f.collection.ID, second.ID}}, 200))
	publicationDrain(t, f)
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 1 {
		t.Fatal("multiple collections duplicated notification", n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_collection_publication_subscriptions s JOIN owned_collection_publication_notifications q ON q.id=s.notification_id WHERE q.app_id=$1`, f.appID); n != 2 {
		t.Fatal(n)
	}
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+p.ID, f.token, `{"content":"edited-secret"}`, 200)
	later := collectionNavigationPost(t, f.collectionNavigationFixture, posts.Public, "already published")
	collectionNavigationAdd(t, f.collectionNavigationFixture, later.ID)
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 1 {
		t.Fatal("edit/add generated publication", n)
	}
	var raw string
	if e := f.db.QueryRow(`SELECT string_agg(data,'') FROM(SELECT to_jsonb(n)::text data FROM notification_v1.inbox n WHERE app_id=$1 UNION ALL SELECT data::text FROM notification_v1.events WHERE app_id=$1) payload`, f.appID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if len(raw) > 4096 || strings.Contains(raw, "secret") || strings.Contains(raw, f.pub.Nick) || strings.Contains(raw, second.ID) {
		t.Fatal("copied or oversized source payload", raw)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.deliveries d JOIN notification_v1.inbox n ON n.id=d.notification_id WHERE n.app_id=$1`, f.appID); n != 0 {
		t.Fatal("external delivery queued", n)
	}
	repo, ctx := publicationReadRepo(t, f)
	page, e := repo.Replay(ctx, f.other, f.appID, 0, 100)
	if e != nil || len(page.Events) != 1 {
		t.Fatal(page, e)
	}
	list := backendDecode[[]notifications.Notification](t, backendCall(t, f.app, "GET", "/api/notifications", f.otherToken, "", 200))
	if len(list) != 1 || list[0].Topic != "posts.new" {
		t.Fatal(list)
	}
}
func TestPGCollectionPublicationDraftScheduleAndEligibility(t *testing.T) {
	f := newCollectionPublicationFixture(t)
	draft := publicationCreate(t, f, map[string]any{"drafted_at": time.Now().UTC().Format(time.RFC3339Nano)})
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_collection_publications WHERE post_id=$1`, draft.ID); n != 0 {
		t.Fatal("draft enqueued", n)
	}
	backendCall(t, f.app, "POST", "/sphere/posts/"+draft.ID+"/publish", f.token, "", 200)
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 1 {
		t.Fatal(n)
	}
	due := scheduleFuture(t, f.db)
	scheduled := publicationCreate(t, f, map[string]any{"published_at": due.Format(time.RFC3339Nano)})
	if scheduled.Schedule == nil {
		t.Fatal(scheduled)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_collection_publications WHERE post_id=$1`, scheduled.ID); n != 0 {
		t.Fatal("future intent enqueued", n)
	}
	scheduleDue(t, f.db, scheduled)
	query(t, f.db, `ALTER TABLE owned_collection_publication_notifications ADD CONSTRAINT publication_schedule_enqueue_failure CHECK(app_id<>'`+f.appID+`') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_collection_publication_notifications DROP CONSTRAINT IF EXISTS publication_schedule_enqueue_failure`)
	})
	if _, e := f.app.ProcessScheduledPosts(context.Background(), 100); e == nil {
		t.Fatal("scheduled enqueue failure hidden")
	}
	var stillDraft bool
	if e := f.db.QueryRow(`SELECT drafted_at IS NOT NULL AND published_at IS NULL FROM owned_text_posts WHERE id=$1`, scheduled.ID).Scan(&stillDraft); e != nil || !stillDraft {
		t.Fatal("scheduled publication survived queue rollback", stillDraft, e)
	}
	scheduleState(t, f.db, scheduled.Schedule.ID, "pending")
	query(t, f.db, `ALTER TABLE owned_collection_publication_notifications DROP CONSTRAINT publication_schedule_enqueue_failure`)
	if _, e := f.app.ProcessScheduledPosts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if _, e := f.app.ProcessScheduledPosts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 2 {
		t.Fatal("due transition missing or duplicate", n)
	}
	for _, v := range []int{posts.Private, posts.Friends, posts.CloseFriends} {
		publicationCreate(t, f, map[string]any{"visibility": v})
	}
	publicationCreate(t, f, map[string]any{"replied_post_id": draft.ID})
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 2 {
		t.Fatal("unsupported subscription audience notified", n)
	}
	publicationCreate(t, f, map[string]any{"visibility": posts.Unlisted})
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 3 {
		t.Fatal("quiet-public source omitted", n)
	}
}
func TestPGCollectionPublicationCurrentAudienceAndIncarnations(t *testing.T) {
	for _, mode := range []string{"unsubscribe", "resubscribe", "collection_delete", "collection_remove", "private", "post_delete", "owner_disabled", "recipient_disabled", "punishment", "publisher_block", "publisher_mute", "recipient_mute", "source_private", "media_deleted", "media_assignment", "owner_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newCollectionPublicationFixture(t)
			var source posts.Post
			values := map[string]any{}
			if mode == "source_private" {
				source = collectionNavigationPost(t, f.collectionNavigationFixture, posts.Public, "private-source-sentinel")
				values["forwarded_post_id"] = source.ID
			}
			p := publicationCreate(t, f, values)
			switch mode {
			case "unsubscribe", "resubscribe":
				backendCall(t, f.app, "POST", f.root+"/unsubscribe", f.otherToken, "", 204)
				if mode == "resubscribe" {
					backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 201)
				}
			case "collection_delete":
				backendCall(t, f.app, "DELETE", f.root, f.token, "", 204)
			case "collection_remove":
				query(t, f.db, `DELETE FROM owned_post_collection_items WHERE collection_id=$1 AND post_id=$2`, f.collection.ID, p.ID)
			case "private":
				backendCall(t, f.app, "PATCH", "/sphere/posts/"+p.ID, f.token, `{"visibility":3}`, 200)
			case "post_delete":
				backendCall(t, f.app, "DELETE", "/sphere/posts/"+p.ID, f.token, "", 204)
			case "owner_disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "recipient_disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
			case "punishment":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()-interval '1 second')`, f.actor)
			case "publisher_block":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/block", f.token, `{}`, 200)
			case "publisher_mute":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/mute", f.token, `{}`, 200)
			case "recipient_mute":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/mute", f.otherToken, `{}`, 200)
			case "source_private":
				backendCall(t, f.app, "PATCH", "/sphere/posts/"+source.ID, f.token, `{"visibility":3}`, 200)
			case "media_deleted":
				file := collectionNavigationFile(t, f.db, f.actor, p.ID)
				query(t, f.db, `UPDATE owned_media_files SET state='deleted',charge='none' WHERE id=$1`, file)
			case "media_assignment":
				collectionNavigationFile(t, f.db, f.actor, p.ID)
				query(t, f.db, `UPDATE owned_post_media SET owner_authorized=false WHERE post_id=$1`, p.ID)
			case "owner_changed":
				query(t, f.db, `UPDATE owned_publishers SET account_id=$2 WHERE id=$1`, f.pub.ID, f.other)
			}
			publicationDrain(t, f)
			if n := publicationInboxCount(t, f); n != 0 {
				t.Fatal("revoked audience received publication", n)
			}
			if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_collection_publication_notifications WHERE app_id=$1 AND processed_at IS NOT NULL AND suppressed_at IS NOT NULL`, f.appID); n != 1 {
				t.Fatal("suppression not durable", n)
			}
		})
	}
}
func TestPGCollectionPublicationInboxReplayRevocation(t *testing.T) {
	for _, mode := range []string{"unsubscribe", "resubscribe", "private", "media", "source", "mute", "collection"} {
		t.Run(mode, func(t *testing.T) {
			f := newCollectionPublicationFixture(t)
			source := collectionNavigationPost(t, f.collectionNavigationFixture, posts.Public, "nested-secret")
			p := publicationCreate(t, f, map[string]any{"forwarded_post_id": source.ID})
			file := collectionNavigationFile(t, f.db, f.actor, p.ID)
			publicationDrain(t, f)
			repo, ctx := publicationReadRepo(t, f)
			page, e := repo.Replay(ctx, f.other, f.appID, 0, 100)
			if e != nil || len(page.Events) != 1 {
				t.Fatal(page, e)
			}
			if n, e := repo.Unread(ctx, f.other, f.appID); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			switch mode {
			case "unsubscribe", "resubscribe":
				backendCall(t, f.app, "POST", f.root+"/unsubscribe", f.otherToken, "", 204)
				if mode == "resubscribe" {
					backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 201)
				}
			case "private":
				backendCall(t, f.app, "PATCH", "/sphere/posts/"+p.ID, f.token, `{"visibility":3}`, 200)
			case "media":
				query(t, f.db, `UPDATE owned_media_files SET state='deleted',charge='none' WHERE id=$1`, file)
			case "source":
				backendCall(t, f.app, "PATCH", "/sphere/posts/"+source.ID, f.token, `{"visibility":3}`, 200)
			case "mute":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/mute", f.token, `{}`, 200)
			case "collection":
				backendCall(t, f.app, "DELETE", f.root, f.token, "", 204)
			}
			if n, e := repo.Unread(ctx, f.other, f.appID); e != nil || n != 0 {
				t.Fatal("revoked count", n, e)
			}
			list, total, e := repo.List(ctx, f.other, f.appID, 100, 0)
			if e != nil || len(list) != 0 || total != 0 {
				t.Fatal("revoked inbox", list, total, e)
			}
			replay, e := repo.Replay(ctx, f.other, f.appID, 0, 100)
			if e != nil || len(replay.Events) != 0 || replay.Cursor != page.HighWater {
				t.Fatal("revoked replay or stalled cursor", replay, e)
			}
			if _, allow, e := repo.RevalidateEvent(ctx, f.other, f.appID, page.Events[0]); e != nil || allow {
				t.Fatal("transport revalidation retained grant", allow, e)
			}
		})
	}
}
func TestPGCollectionPublicationRollbackNamespaceAndConcurrentWorkers(t *testing.T) {
	f := newCollectionPublicationFixture(t)
	foreign := newCollectionPublicationFixture(t)
	publicationCreate(t, foreign, nil)
	query(t, f.db, `ALTER TABLE owned_collection_publication_notifications ADD CONSTRAINT publication_enqueue_failure CHECK(app_id<>'`+f.appID+`')`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_collection_publication_notifications DROP CONSTRAINT IF EXISTS publication_enqueue_failure`)
	})
	before := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID)
	backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": "rollback body", "collection_ids": []string{f.collection.ID}}, 503)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID); n != before {
		t.Fatal("publication survived enqueue failure", n, before)
	}
	query(t, f.db, `ALTER TABLE owned_collection_publication_notifications DROP CONSTRAINT publication_enqueue_failure`)
	publicationCreate(t, f, nil)
	query(t, f.db, `ALTER TABLE owned_collection_publication_notifications ADD CONSTRAINT publication_receipt_failure CHECK(app_id<>'`+f.appID+`' OR processed_at IS NULL)`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_collection_publication_notifications DROP CONSTRAINT IF EXISTS publication_receipt_failure`)
	})
	if _, e := f.app.ProcessCollectionPublicationNotifications(context.Background(), 100); e == nil {
		t.Fatal("receipt failure hidden")
	}
	if n := publicationInboxCount(t, f); n != 0 {
		t.Fatal("partial inbox", n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.events WHERE app_id=$1`, f.appID); n != 0 {
		t.Fatal("partial replay", n)
	}
	query(t, f.db, `ALTER TABLE owned_collection_publication_notifications DROP CONSTRAINT publication_receipt_failure`)
	db2 := database(t)
	app2 := surveySubscriptionBackend(t, db2, f.config, f.appID)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, run := range []func(context.Context, int) (int, error){f.app.ProcessCollectionPublicationNotifications, app2.ProcessCollectionPublicationNotifications} {
		wg.Add(1)
		go func(fn func(context.Context, int) (int, error)) {
			defer wg.Done()
			_, e := fn(context.Background(), 100)
			errs <- e
		}(run)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 1 {
		t.Fatal("concurrent fanout duplicate/loss", n)
	}
	if n := publicationInboxCount(t, foreign); n != 0 {
		t.Fatal("foreign namespace consumed", n)
	}
	publicationDrain(t, foreign)
	if n := publicationInboxCount(t, foreign); n != 1 {
		t.Fatal(n)
	}
}
func TestPGCollectionPublicationWaitRechecksSourcesAndCancellation(t *testing.T) {
	for _, mode := range []string{"media", "owner", "assignment", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newCollectionPublicationFixture(t)
			p := publicationCreate(t, f, nil)
			file := collectionNavigationFile(t, f.db, f.actor, p.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "owned_media_files", file
			if mode == "owner" {
				table, id = "owned_authority_accounts", f.actor
			}
			blocker, pid := atomicBlocker(t, locks, table, id)
			done := surveyWorker(t, cancel, blocker, func() error { _, e := f.app.ProcessCollectionPublicationNotifications(ctx, 100); return e })
			waitAtomicBlocked(t, locks, pid, table)
			switch mode {
			case "media":
				if _, e := blocker.ExecContext(ctx, `UPDATE owned_media_files SET state='deleted',charge='none' WHERE id=$1`, file); e != nil {
					t.Fatal(e)
				}
			case "owner":
				if _, e := blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor); e != nil {
					t.Fatal(e)
				}
			case "assignment":
				if _, e := blocker.ExecContext(ctx, `UPDATE owned_post_media SET owner_authorized=false WHERE post_id=$1`, p.ID); e != nil {
					t.Fatal(e)
				}
			case "cancel":
				cancel()
			}
			if e := blocker.Commit(); e != nil && mode != "cancel" {
				t.Fatal(e)
			}
			e := <-done
			if mode == "cancel" || mode == "assignment" {
				if e == nil {
					t.Fatal("changed dependency/cancel accepted")
				}
			} else if e != nil {
				t.Fatal(e)
			}
			if n := publicationInboxCount(t, f); n != 0 {
				t.Fatal("stale publication", n)
			}
			// A subsequent bounded call proves transaction cleanup and retries safely.
			publicationDrain(t, f)
			want := 0
			if mode == "cancel" {
				want = 1
			}
			if n := publicationInboxCount(t, f); n != want {
				t.Fatal(n, want)
			}
		})
	}
}
func TestPGCollectionPublicationSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "collection-publication.json"
	type saved struct {
		Actor, Other, Token, OtherToken, App, Post, Root, Original string
		Authority                                                  restartAuthoritySnapshot
	}
	var s saved
	if phase == "before" {
		f := newCollectionPublicationFixture(t)
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f.config.Now = func() time.Time { return time.Now().UTC() }
		issuer, e := authority.New(authoritypg.New(db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.token = pgIssue(t, issuer, f.actor).Token
		f.otherToken = pgIssue(t, issuer, f.other).Token
		f.app = surveySubscriptionBackend(t, db, f.config, f.appID)
		p := publicationCreate(t, f, nil)
		original := compositionAuthorityState(t, db, f.actor, f.other)
		s = saved{f.actor, f.other, f.token, f.otherToken, f.appID, p.ID, f.root, original, saveAuthoritySnapshot(t, f.config)}
		saveRestartFixture(t, marker, s)
		return
	}
	loadRestartFixture(t, marker, &s)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	cfg := restoreAuthoritySnapshot(t, s.Authority)
	app := surveySubscriptionBackend(t, db, cfg, s.App)
	if compositionAuthorityState(t, db, s.Actor, s.Other) != s.Original {
		t.Fatal("original authority changed")
	}
	for range 2 {
		if _, e := app.ProcessCollectionPublicationNotifications(context.Background(), 100); e != nil {
			t.Fatal(e)
		}
	}
	f := collectionPublicationFixture{collectionNavigationFixture: collectionNavigationFixture{backendFixture: backendFixture{db: db, config: cfg, actor: s.Actor, other: s.Other, token: s.Token, otherToken: s.OtherToken, app: app}, root: s.Root}, appID: s.App}
	repo, ctx := publicationReadRepo(t, f)
	page, e := repo.Replay(ctx, s.Other, s.App, 0, 100)
	if e != nil || len(page.Events) != 1 {
		t.Fatal(page, e)
	}
	raw, _ := json.Marshal(page)
	if !strings.Contains(string(raw), s.Post) || strings.Contains(string(raw), "private-body-sentinel") {
		t.Fatal(string(raw))
	}
	backendCall(t, app, "POST", s.Root+"/unsubscribe", s.OtherToken, "", 204)
	page, e = repo.Replay(ctx, s.Other, s.App, 0, 100)
	if e != nil || len(page.Events) != 0 {
		t.Fatal("restart replay ignored revoke", page, e)
	}
	if n := publicationInboxCount(t, f); n != 1 {
		t.Fatal("restart projection duplicate", n)
	}
}

func TestPGCollectionPublicationRealmMembershipAndOwnerPolicy(t *testing.T) {
	base := postRealmFixture(t)
	namespace := "collection.realm." + base.actor
	app := surveySubscriptionBackend(t, base.db, base.config, namespace)
	c := backendDecode[posts.Collection](t, backendCall(t, app, "POST", "/sphere/publishers/"+base.individual.Name+"/collections", base.token, `{"slug":"publication-realm"}`, 201))
	root := "/sphere/publishers/" + base.individual.Name + "/collections/" + c.Slug
	backendCall(t, app, "POST", root+"/subscribe", base.otherToken, "", 201)
	f := collectionPublicationFixture{collectionNavigationFixture: collectionNavigationFixture{backendFixture: backendFixture{db: base.db, config: base.config, actor: base.actor, other: base.other, token: base.token, otherToken: base.otherToken, app: app}, pub: base.individual, collection: c, root: root}, appID: namespace}
	publicationCreate(t, f, map[string]any{"realm_id": base.private.ID})
	publicationDrain(t, f)
	repo, ctx := publicationReadRepo(t, f)
	page, e := repo.Replay(ctx, f.other, namespace, 0, 100)
	if e != nil || len(page.Events) != 1 {
		t.Fatal(page, e)
	}
	if _, e = base.social.ChangeMember(base.otherCtx, base.other, base.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	page, e = repo.Replay(ctx, f.other, namespace, 0, 100)
	if e != nil || len(page.Events) != 0 {
		t.Fatal("realm membership revoke leaked replay", page, e)
	}
	publicationCreate(t, f, map[string]any{"realm_id": base.private.ID})
	publicationDrain(t, f)
	if n := publicationInboxCount(t, f); n != 1 {
		t.Fatal("former realm member received new item", n)
	}
}
func TestPGCollectionPublicationBoundsAndProofAfterWait(t *testing.T) {
	f := newCollectionPublicationFixture(t)
	for _, limit := range []int{-1, 0, 101} {
		if _, e := f.app.ProcessCollectionPublicationNotifications(context.Background(), limit); !errors.Is(e, posts.ErrInvalid) {
			t.Fatal(limit, e)
		}
	}
	publicationCreate(t, f, nil)
	publicationDrain(t, f)
	repo, bound := publicationReadRepo(t, f)
	ctx, cancel := context.WithTimeout(bound, 15*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.other)
	done := surveyWorker(t, cancel, blocker, func() error { _, e := repo.Replay(ctx, f.other, f.appID, 0, 100); return e })
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("original proof survived account wait revocation")
	}
	// The bounded publication snapshot is all-or-nothing, never silently truncated.
	capped := newCollectionPublicationFixture(t)
	query(t, capped.db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at) SELECT md5($1::text||':'||g::text)::uuid,'publication-cap-'||$1::text||'-'||g::text,'synthetic',clock_timestamp() FROM generate_series(1,$2::integer)g`, capped.actor, interactionpg.MaxPublicationSubscriptions)
	query(t, capped.db, `INSERT INTO owned_taxonomy.post_category_subscriptions(id,account_id,collection_id,created_at,updated_at) SELECT md5($1::text||':subscription:'||g::text)::uuid,md5($1::text||':'||g::text)::uuid,$2,clock_timestamp(),clock_timestamp() FROM generate_series(1,$3::integer)g`, capped.actor, capped.collection.ID, interactionpg.MaxPublicationSubscriptions)
	before := subscriptionCount(t, capped.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, capped.pub.ID)
	backendJSONCall(t, capped.app, "POST", "/sphere/posts?pub="+capped.pub.ID, capped.token, map[string]any{"content": "oversized audience", "collection_ids": []string{capped.collection.ID}}, 409)
	if n := subscriptionCount(t, capped.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, capped.pub.ID); n != before {
		t.Fatal("fanout limit left partial publication", n, before)
	}
}

func TestPGCollectionPublicationFinalEnqueueWaitRechecksComposition(t *testing.T) {
	for _, mode := range []string{"create", "draft_publish"} {
		t.Run(mode, func(t *testing.T) {
			f := newCollectionPublicationFixture(t)
			survey := authorityUUID(t)
			query(t, f.db, `INSERT INTO owned_surveys(id,publisher_id,title,is_anonymous,hide_results,status,questions,published_at,created_at,updated_at)VALUES($1,$2,'Synthetic publication reference',false,false,1,'[{"id":"q","type":0,"title":"Question","is_required":false}]',clock_timestamp(),clock_timestamp(),clock_timestamp())`, survey, f.pub.ID)
			values := map[string]any{"content": "enqueue deadline rollback", "collection_ids": []string{f.collection.ID}, "embeds": []posts.Embed{{Type: "survey", ID: survey}}}
			path := "/sphere/posts?pub=" + f.pub.ID
			var draft posts.Post
			if mode == "draft_publish" {
				values["drafted_at"] = time.Now().UTC()
				draft = publicationCreate(t, f, values)
				path = "/sphere/posts/" + draft.ID + "/publish"
				values = nil
			}
			before := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID)
			beforeAudit := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit a JOIN owned_text_posts p ON p.id=a.post_id WHERE p.publisher_id=$1`, f.pub.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			blocker, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.ExecContext(ctx, `LOCK TABLE owned_collection_publication_notifications IN SHARE MODE`); e != nil {
				t.Fatal(e)
			}
			var expiration time.Time
			if e = f.db.QueryRow(`UPDATE owned_surveys SET ended_at=clock_timestamp()+interval '5 seconds' WHERE id=$1 RETURNING ended_at`, survey).Scan(&expiration); e != nil {
				t.Fatal(e)
			}
			req := fixtureNoBodyRequest("POST", path, f.token)
			if values != nil {
				body, e := json.Marshal(values)
				if e != nil {
					t.Fatal(e)
				}
				req = fixtureRawJSONRequest("POST", path, f.token, string(body))
			}
			req = req.WithContext(ctx)
			result := httptest.NewRecorder()
			done := surveyWorker(t, cancel, blocker, func() error {
				f.app.ServeHTTP(result, req)
				return nil
			})
			moderationWait(t, ctx, f.db, pid, "owned_collection_publication_notifications")
			var current bool
			if e = f.db.QueryRow(`SELECT clock_timestamp()<$1`, expiration).Scan(&current); e != nil || !current {
				t.Fatal("source expired before the final enqueue wait", current, e)
			}
			waitPostScheduleClock(t, f.db, expiration)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e != nil || result.Code != 404 {
				t.Fatal("expired reference survived enqueue wait", result.Code, result.Body.String(), e)
			}
			if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID); n != before {
				t.Fatal("post escaped rollback", n, before)
			}
			if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit a JOIN owned_text_posts p ON p.id=a.post_id WHERE p.publisher_id=$1`, f.pub.ID); n != beforeAudit {
				t.Fatal("audit escaped rollback", n, beforeAudit)
			}
			if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_collection_publications WHERE publisher_id=$1`, f.pub.ID); n != 0 {
				t.Fatal("publication intent escaped rollback", n)
			}
			if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_collection_publication_notifications WHERE app_id=$1`, f.appID); n != 0 {
				t.Fatal("recipient snapshot escaped rollback", n)
			}
			if mode == "draft_publish" {
				var stillDraft bool
				if e = f.db.QueryRow(`SELECT drafted_at IS NOT NULL AND published_at IS NULL FROM owned_text_posts WHERE id=$1`, draft.ID).Scan(&stillDraft); e != nil || !stillDraft {
					t.Fatal("draft publication escaped rollback", stillDraft, e)
				}
			}
		})
	}
}
