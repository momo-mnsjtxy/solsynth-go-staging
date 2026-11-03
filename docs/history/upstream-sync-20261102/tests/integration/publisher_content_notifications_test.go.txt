// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/publishernotificationpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

func contentNotificationSubscribe(t *testing.T, f backendFixture, p accounts.Publisher) {
	t.Helper()
	root := "/sphere/publishers/" + p.Name
	backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200)
	backendCall(t, f.app, "PATCH", root+"/subscription/me/notify", f.otherToken, `{"notify":true}`, 200)
}

func contentNotificationPost(t *testing.T, f backendFixture, p accounts.Publisher, visibility int, content string) posts.Post {
	t.Helper()
	return backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+p.ID, f.token, fmt.Sprintf(`{"content":%q,"visibility":%d}`, content, visibility), 200))
}

func drainPublisherContent(t *testing.T, workers ...*application.Backend) {
	t.Helper()
	for range 100 {
		total := 0
		for _, worker := range workers {
			n, e := worker.ProcessPublisherContentNotifications(context.Background(), 100)
			if e != nil {
				t.Fatal(e)
			}
			total += n
		}
		if total == 0 {
			return
		}
	}
	t.Fatal("publisher notification projector did not become idle")
}

func contentOutboxCount(t *testing.T, f backendFixture, source string) int {
	t.Helper()
	return subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'source_id'=$2`, f.other, source)
}

func TestPGPublisherContentPostAudienceAndIdempotence(t *testing.T) {
	f := newBackendFixture(t)
	p := subscriptionPublisher(t, f)
	contentNotificationSubscribe(t, f, p)

	public := contentNotificationPost(t, f, p, posts.Public, "public subscriber content")
	second := backendApp(t, f.db, f.config)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	wg.Add(2)
	for _, worker := range []*application.Backend{f.app, second} {
		go func(worker *application.Backend) {
			defer wg.Done()
			for range 3 {
				_, e := worker.ProcessPublisherContentNotifications(context.Background(), 100)
				errs <- e
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	drainPublisherContent(t, f.app, second)
	if n := contentOutboxCount(t, f, public.ID); n != 1 {
		t.Fatal("two-worker duplicate", n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_content_notification_intents WHERE source_id=$1`, public.ID); n != 1 {
		t.Fatal("publication intent duplicate", n)
	}
	query(t, f.db, `UPDATE owned_publishers SET nick=$2 WHERE id=$1`, p.ID, strings.Repeat("🙂", 256))
	wide := contentNotificationPost(t, f, p, posts.Public, strings.Repeat("界", 2049))
	following := contentNotificationPost(t, f, p, posts.Public, "after multibyte payload")
	drainPublisherContent(t, f.app, second)
	for _, source := range []string{wide.ID, following.ID} {
		if n := contentOutboxCount(t, f, source); n != 1 {
			t.Fatal("multibyte intent blocked queue", source, n)
		}
	}
	var wideRaw []byte
	if e := f.db.QueryRow(`SELECT command FROM notification_v1.outbox WHERE command->'Meta'->>'post_id'=$1`, wide.ID).Scan(&wideRaw); e != nil {
		t.Fatal(e)
	}
	var wideCommand notifications.Command
	if e := json.Unmarshal(wideRaw, &wideCommand); e != nil || len(wideCommand.Title) > 1024 || len(wideCommand.Content) > 512 || !utf8.ValidString(wideCommand.Title) || !utf8.ValidString(wideCommand.Content) {
		t.Fatal("invalid multibyte payload", wideCommand, e)
	}
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+public.ID, f.token, `{"content":"ordinary edit"}`, 200)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_content_notification_intents WHERE source_id=$1`, public.ID); n != 1 {
		t.Fatal("edit emitted publication", n)
	}
	locked := contentNotificationPost(t, f, p, posts.Public, "locked but readable")
	query(t, f.db, `UPDATE owned_text_posts SET locked_at=clock_timestamp() WHERE id=$1`, locked.ID)
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, locked.ID); n != 1 {
		t.Fatal("readable locked post suppressed", n)
	}

	backendCall(t, f.app, "PATCH", "/sphere/publishers/"+p.Name+"/subscription/me/notify", f.otherToken, `{"notify":false}`, 200)
	notifyOff := contentNotificationPost(t, f, p, posts.Public, "notify off")
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, notifyOff.ID); n != 0 {
		t.Fatal("notify-off delivery", n)
	}
	backendCall(t, f.app, "PATCH", "/sphere/publishers/"+p.Name+"/subscription/me/notify", f.otherToken, `{"notify":true}`, 200)

	blocked := contentNotificationPost(t, f, p, posts.Public, "blocked before projection")
	query(t, f.db, `INSERT INTO owned_social_effective_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,-100,clock_timestamp(),clock_timestamp()) ON CONFLICT(account_id,related_id)DO UPDATE SET status=-100,updated_at=clock_timestamp()`, authorityUUID(t), f.other, f.actor)
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, blocked.ID); n != 0 {
		t.Fatal("blocked delivery", n)
	}
	query(t, f.db, `DELETE FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2`, f.other, f.actor)

	muted := contentNotificationPost(t, f, p, posts.Public, "muted before projection")
	query(t, f.db, `INSERT INTO owned_social_effective_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,-50,clock_timestamp(),clock_timestamp()) ON CONFLICT(account_id,related_id)DO UPDATE SET status=-50,updated_at=clock_timestamp()`, authorityUUID(t), f.actor, f.other)
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, muted.ID); n != 0 {
		t.Fatal("publisher-muted-recipient delivery", n)
	}
	query(t, f.db, `DELETE FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
}

func TestPGPublisherContentCloseFriendRevocationAndSubscriptionABA(t *testing.T) {
	f := newBackendFixture(t)
	p := subscriptionPublisher(t, f)
	contentNotificationSubscribe(t, f, p)
	for _, row := range []struct {
		a, b   string
		status int
	}{{f.actor, f.other, 200}, {f.other, f.actor, 100}} {
		query(t, f.db, `INSERT INTO owned_social_effective_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,$4,clock_timestamp(),clock_timestamp())`, authorityUUID(t), row.a, row.b, row.status)
	}
	closePost := contentNotificationPost(t, f, p, posts.CloseFriends, "close friend content")
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, closePost.ID); n != 1 {
		t.Fatal("close-friend notification", n)
	}
	var raw []byte
	if e := f.db.QueryRow(`SELECT command FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'post_id'=$2`, f.other, closePost.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var command notifications.Command
	if e := json.Unmarshal(raw, &command); e != nil || command.Topic != "posts.close_friends" || command.Content != "close friend content" || !strings.Contains(string(command.Meta), `"notification_type":"close_friends"`) {
		t.Fatal(command, e)
	}
	lostAudience := contentNotificationPost(t, f, p, posts.CloseFriends, "lost close friend audience")
	query(t, f.db, `DELETE FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, lostAudience.ID); n != 0 {
		t.Fatal("stale close-friend delivery", n)
	}
	privatePost := contentNotificationPost(t, f, p, posts.Private, "private content")
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, privatePost.ID); n != 0 {
		t.Fatal("private delivery", n)
	}

	revoked := contentNotificationPost(t, f, p, posts.Public, "revoked subscription")
	backendCall(t, f.app, "POST", "/sphere/publishers/"+p.Name+"/unsubscribe", f.otherToken, "", 204)
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, revoked.ID); n != 0 {
		t.Fatal("revoked delivery", n)
	}

	contentNotificationSubscribe(t, f, p)
	var incarnation int64
	if e := f.db.QueryRow(`SELECT incarnation FROM owned_publisher_follows WHERE account_id=$1 AND publisher_id=$2`, f.other, p.ID).Scan(&incarnation); e != nil {
		t.Fatal(e)
	}
	aba := contentNotificationPost(t, f, p, posts.Public, "subscription ABA")
	backendCall(t, f.app, "POST", "/sphere/publishers/"+p.Name+"/unsubscribe", f.otherToken, "", 204)
	contentNotificationSubscribe(t, f, p)
	var current int64
	if e := f.db.QueryRow(`SELECT incarnation FROM owned_publisher_follows WHERE account_id=$1 AND publisher_id=$2`, f.other, p.ID).Scan(&current); e != nil || current <= incarnation {
		t.Fatal("incarnation did not advance", incarnation, current, e)
	}
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, aba.ID); n != 0 {
		t.Fatal("ABA delivery", n)
	}
}

func TestPGPublisherContentWaitRechecksCurrentAuthority(t *testing.T) {
	f := newBackendFixture(t)
	p := subscriptionPublisher(t, f)
	contentNotificationSubscribe(t, f, p)
	post := contentNotificationPost(t, f, p, posts.Public, "wait current authority")
	tx, e := f.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`SELECT 1 FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, x := f.app.ProcessPublisherContentNotifications(ctx, 100); done <- x }()
	select {
	case e = <-done:
		t.Fatal("worker did not wait", e)
	case <-time.After(100 * time.Millisecond):
	}
	if _, e = tx.Exec(`UPDATE owned_publisher_follows SET notify=false,updated_at=clock_timestamp() WHERE account_id=$1 AND publisher_id=$2`, f.other, p.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, post.ID); n != 0 {
		t.Fatal("stale authority delivery", n)
	}
}

func TestPGPublisherContentScheduledPublication(t *testing.T) {
	f := newBackendFixture(t)
	p := subscriptionPublisher(t, f)
	contentNotificationSubscribe(t, f, p)
	due := time.Now().UTC().Add(time.Hour)
	post := backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+p.ID, f.token, fmt.Sprintf(`{"content":"scheduled subscriber content","published_at":%q}`, due.Format(time.RFC3339Nano)), 200))
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_content_notification_intents WHERE source_id=$1`, post.ID); n != 0 {
		t.Fatal("early schedule intent", n)
	}
	query(t, f.db, `UPDATE owned_post_schedules SET due_at=clock_timestamp() WHERE post_id=$1`, post.ID)
	if n, e := f.app.ProcessScheduledPosts(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, post.ID); n != 1 {
		t.Fatal("scheduled notification", n)
	}
}

func newPublisherNotificationFixtureInDatabase(t *testing.T, db *sql.DB) backendFixture {
	t.Helper()
	keys := authorityKeys(t)
	clock := &atomic.Int64{}
	clock.Store(time.Now().UTC().Unix())
	config := authorityConfig(t, keys, clock)
	config.AccessTTL, config.SessionTTL = time.Hour, time.Hour
	config.Now = func() time.Time { return time.Now().UTC() }
	f := backendFixture{db: db, keys: keys, config: config, actor: authorityUUID(t), other: authorityUUID(t)}
	for _, id := range []string{f.actor, f.other} {
		if e := authoritypg.SeedSynthetic(context.Background(), db, id, "synthetic_publisher_notification_"+strings.ReplaceAll(id, "-", ""), time.Now().UTC()); e != nil {
			t.Fatal(e)
		}
		if e := authoritypg.New(db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	var e error
	f.issuer, e = authority.New(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	f.token, f.otherToken = pgIssue(t, f.issuer, f.actor).Token, pgIssue(t, f.issuer, f.other).Token
	f.app = backendApp(t, db, config)
	return f
}

func publisherNotificationRestartSnapshot(t *testing.T, db *sql.DB, post string) string {
	t.Helper()
	var out string
	e := db.QueryRow(`SELECT jsonb_build_object(
	 'intent',(SELECT to_jsonb(i) FROM owned_publisher_content_notification_intents i WHERE i.source_id=$1),
	 'post',(SELECT jsonb_build_object('id',p.id,'publisher_id',p.publisher_id,'published_at',p.published_at,'visibility',p.visibility,'deleted_at',p.deleted_at) FROM owned_text_posts p WHERE p.id=$1),
	 'publisher',(SELECT jsonb_build_object('id',u.id,'account_id',u.account_id,'realm_id',u.realm_id,'deleted_at',u.deleted_at) FROM owned_publishers u JOIN owned_text_posts p ON p.publisher_id=u.id WHERE p.id=$1),
	 'follow',(SELECT jsonb_build_object('id',f.id,'account_id',f.account_id,'publisher_id',f.publisher_id,'state',f.state,'notify',f.notify,'incarnation',f.incarnation,'incarnation_started_at',f.incarnation_started_at,'ended_at',f.ended_at) FROM owned_publisher_follows f JOIN owned_text_posts p ON p.publisher_id=f.publisher_id WHERE p.id=$1)
	 )::text`, post).Scan(&out)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestPGPublisherContentSavedRestartPersistence(t *testing.T) {
	_ = database(t)
	const marker = "publisher-content-notifications.json"
	type saved struct {
		Actor, Other, Token, OtherToken, Publisher, Post string
		IntentJSON, PublicSnapshot                       string
		Authority                                        restartAuthoritySnapshot
		Database                                         isolatedRestartDatabase
	}
	if fixtureRestartPhase(t) == "before" {
		db, identity := createIsolatedRestartDatabase(t, "pubnotify")
		f := newPublisherNotificationFixtureInDatabase(t, db)
		p := subscriptionPublisher(t, f)
		contentNotificationSubscribe(t, f, p)
		post := contentNotificationPost(t, f, p, posts.Public, "restart pending intent")
		var intentJSON string
		if e := db.QueryRow(`SELECT to_jsonb(i)::text FROM owned_publisher_content_notification_intents i WHERE source_id=$1 AND completed_at IS NULL`, post.ID).Scan(&intentJSON); e != nil {
			t.Fatal(e)
		}
		if n := contentOutboxCount(t, f, post.ID); n != 0 {
			t.Fatal("projected before restart", n)
		}
		state := saved{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Publisher: p.ID, Post: post.ID, IntentJSON: intentJSON, PublicSnapshot: publisherNotificationRestartSnapshot(t, db, post.ID), Authority: saveAuthoritySnapshot(t, f.config), Database: identity}
		saveRestartFixture(t, marker, state)
		return
	}
	var state saved
	loadRestartFixture(t, marker, &state)
	db := openIsolatedRestartDatabase(t, state.Database)
	var intentJSON string
	if e := db.QueryRow(`SELECT to_jsonb(i)::text FROM owned_publisher_content_notification_intents i WHERE source_id=$1 AND completed_at IS NULL`, state.Post).Scan(&intentJSON); e != nil || intentJSON != state.IntentJSON {
		t.Fatal("original pending intent changed", e)
	}
	if got := publisherNotificationRestartSnapshot(t, db, state.Post); got != state.PublicSnapshot {
		t.Fatal("original public snapshot changed")
	}
	if n := subscriptionCount(t, db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'source_id'=$2`, state.Other, state.Post); n != 0 {
		t.Fatal("isolated intent consumed before restart", n)
	}
	cfg := restoreAuthoritySnapshot(t, state.Authority)
	first, second := backendApp(t, db, cfg), backendApp(t, db, cfg)
	drainPublisherContent(t, first, second)
	if n := subscriptionCount(t, db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'source_id'=$2`, state.Other, state.Post); n != 1 {
		t.Fatal("restart delivery", n)
	}
	if n := subscriptionCount(t, db, `SELECT count(*) FROM owned_publisher_content_notification_intents WHERE source_id=$1 AND completed_at IS NOT NULL`, state.Post); n != 1 {
		t.Fatal("restart completion", n)
	}
}

type publisherNotificationSender struct {
	db      *sql.DB
	account string
	calls   int
	items   []notifications.Delivery
}

func (s *publisherNotificationSender) Send(ctx context.Context, d notifications.Delivery) error {
	// Dispatch must commit its lease and release all domain locks before provider I/O.
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE NOWAIT`, s.account).Scan(&held); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.calls++
	s.items = append(s.items, d)
	return nil
}

func publisherNotificationBackend(t *testing.T, f backendFixture, sender notifications.Sender) *application.Backend {
	t.Helper()
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, NotificationSender: sender})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return app
}

func publisherNotificationReadRepo(t *testing.T, f backendFixture) (*notificationpg.Repository, context.Context) {
	t.Helper()
	v, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := v.Authenticate(context.Background(), f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), v)
	repo := notificationpg.New(f.db, guard, notifications.DefaultApp)
	policy := publishernotificationpg.New(f.db, socialpg.New(f.db), repo, notifications.DefaultApp)
	repo.WithPublisherContent(policy)
	return repo, guard.Bind(context.Background(), proof)
}

func TestPGPublisherContentInboxReplayAndProviderRecheck(t *testing.T) {
	f := newBackendFixture(t)
	p := subscriptionPublisher(t, f)
	contentNotificationSubscribe(t, f, p)
	post := contentNotificationPost(t, f, p, posts.Public, "exact provider payload body")
	drainPublisherContent(t, f.app)
	repo, readCtx := publisherNotificationReadRepo(t, f)
	if n, e := repo.ProcessPending(context.Background(), 100); e != nil || n < 1 {
		t.Fatal(n, e)
	}
	list, total, e := repo.List(readCtx, f.other, notifications.DefaultApp, 100, 0)
	if e != nil || total != 1 || len(list) != 1 || list[0].Content != "exact provider payload body" {
		t.Fatal(list, total, e)
	}
	var meta map[string]any
	if e = json.Unmarshal(list[0].Meta, &meta); e != nil || meta["post_id"] != post.ID || meta["action_uri"] != "/posts/"+post.ID || meta["notification_type"] != "subscription" {
		t.Fatal(meta, e)
	}
	page, e := repo.Replay(readCtx, f.other, notifications.DefaultApp, 0, 100)
	if e != nil || len(page.Events) != 1 {
		t.Fatal(page, e)
	}
	assertVisible := func(label string, want bool) {
		t.Helper()
		list, total, x := repo.List(readCtx, f.other, notifications.DefaultApp, 100, 0)
		if x != nil || (total > 0) != want || (len(list) > 0) != want {
			t.Fatal(label, list, total, x)
		}
		page, x := repo.Replay(readCtx, f.other, notifications.DefaultApp, 0, 100)
		if x != nil || (len(page.Events) > 0) != want {
			t.Fatal(label+" replay", page, x)
		}
	}
	query(t, f.db, `UPDATE owned_publisher_follows SET notify=false WHERE account_id=$1 AND publisher_id=$2`, f.other, p.ID)
	assertVisible("notify revoked", false)
	query(t, f.db, `UPDATE owned_publisher_follows SET notify=true WHERE account_id=$1 AND publisher_id=$2`, f.other, p.ID)
	assertVisible("notify restored", true)
	query(t, f.db, `INSERT INTO owned_social_effective_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,-100,clock_timestamp(),clock_timestamp())`, authorityUUID(t), f.other, f.actor)
	assertVisible("blocked", false)
	query(t, f.db, `DELETE FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2`, f.other, f.actor)
	assertVisible("block removed", true)
	query(t, f.db, `INSERT INTO owned_social_effective_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,-50,clock_timestamp(),clock_timestamp())`, authorityUUID(t), f.other, f.actor)
	assertVisible("muted", false)
	query(t, f.db, `DELETE FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2`, f.other, f.actor)
	assertVisible("mute removed", true)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	assertVisible("publisher owner disabled", false)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
	assertVisible("publisher owner restored", true)
	query(t, f.db, `UPDATE owned_text_posts SET visibility=3 WHERE id=$1`, post.ID)
	list, total, e = repo.List(readCtx, f.other, notifications.DefaultApp, 100, 0)
	if e != nil || total != 0 || len(list) != 0 {
		t.Fatal("private source inbox visible", list, total, e)
	}
	page, e = repo.Replay(readCtx, f.other, notifications.DefaultApp, 0, 100)
	if e != nil || len(page.Events) != 0 {
		t.Fatal("private source replay visible", page, e)
	}
	query(t, f.db, `UPDATE owned_text_posts SET visibility=0 WHERE id=$1`, post.ID)

	// A second projected item is revoked after inbox creation but before device send.
	revoked := contentNotificationPost(t, f, p, posts.Public, "revoked before device send")
	drainPublisherContent(t, f.app)
	if _, e = repo.ProcessPending(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", "/sphere/publishers/"+p.Name+"/unsubscribe", f.otherToken, "", 204)
	list, total, e = repo.List(readCtx, f.other, notifications.DefaultApp, 100, 0)
	if e != nil || total != 0 || len(list) != 0 {
		t.Fatal("revoked inbox visible", list, total, e)
	}
	page, e = repo.Replay(readCtx, f.other, notifications.DefaultApp, 0, 100)
	if e != nil || len(page.Events) != 0 {
		t.Fatal("revoked replay visible", page, e)
	}
	sender := &publisherNotificationSender{db: f.db, account: f.other}
	for range 4 {
		_, e = repo.DispatchOne(context.Background(), sender)
		if e != nil {
			t.Fatal(e)
		}
	}
	if sender.calls != 0 {
		t.Fatal("revoked provider delivery", sender.calls, revoked.ID)
	}
}

func TestPGPublisherContentProviderRunsAfterDatabaseLocks(t *testing.T) {
	f := newBackendFixture(t)
	p := subscriptionPublisher(t, f)
	contentNotificationSubscribe(t, f, p)
	post := contentNotificationPost(t, f, p, posts.Public, "provider positive")
	drainPublisherContent(t, f.app)
	sender := &publisherNotificationSender{db: f.db, account: f.other}
	app := publisherNotificationBackend(t, f, sender)
	for range 5 {
		if _, e := app.ProcessNotificationStep(context.Background()); e != nil {
			t.Fatal(e)
		}
		if sender.calls == 1 {
			break
		}
	}
	if sender.calls != 1 {
		t.Fatal("configured provider was not used", sender.calls)
	}
	if sender.items[0].Notification.Content != "provider positive" || !strings.Contains(string(sender.items[0].Notification.Meta), post.ID) {
		t.Fatal(sender.items[0])
	}
}

func TestPGPublisherContentPendingApprovalStartsAudienceAtAcceptance(t *testing.T) {
	f := newBackendFixture(t)
	p := subscriptionPublisher(t, f)
	query(t, f.db, `INSERT INTO owned_publisher_follow_policy(publisher_id,requires_approval)VALUES($1,true)`, p.ID)
	root := "/sphere/publishers/" + p.Name
	pending := backendDecode[timeline.SubscriptionStatus](t, backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200))
	beforeFirstWorker := contentNotificationPost(t, f, p, posts.Public, "before approval first ordering")
	drainPublisherContent(t, f.app)
	backendCall(t, f.app, "POST", root+"/subscription/requests/"+pending.Subscription.ID+"/approve", f.token, "", 200)
	if n := contentOutboxCount(t, f, beforeFirstWorker.ID); n != 0 {
		t.Fatal("pending recipient delivered", n)
	}
	backendCall(t, f.app, "POST", root+"/unsubscribe", f.otherToken, "", 204)
	pending = backendDecode[timeline.SubscriptionStatus](t, backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200))
	beforeApproval := contentNotificationPost(t, f, p, posts.Public, "before approval second ordering")
	backendCall(t, f.app, "POST", root+"/subscription/requests/"+pending.Subscription.ID+"/approve", f.token, "", 200)
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, beforeApproval.ID); n != 0 {
		t.Fatal("pre-acceptance event delivered", n)
	}
	afterApproval := contentNotificationPost(t, f, p, posts.Public, "after approval")
	drainPublisherContent(t, f.app)
	if n := contentOutboxCount(t, f, afterApproval.ID); n != 1 {
		t.Fatal("accepted audience missing", n)
	}
}

func TestPGPublisherContentPrivatePostRealmAndTimestampPrecision(t *testing.T) {
	f := postRealmFixture(t)
	app := "publisher.realm.notifications"
	sink := notificationpg.New(f.db, f.guard, app)
	policy := publishernotificationpg.New(f.db, f.social, sink, app)
	sink.WithPublisherContent(policy)
	f.posts.WithPublicationObserver(policy)
	follow := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_publisher_follows(id,account_id,publisher_id,state,notify,created_at,updated_at,incarnation,incarnation_started_at)VALUES($1,$2,$3,'accepted',true,clock_timestamp(),clock_timestamp(),1,clock_timestamp())`, follow, f.outside, f.individual.ID)
	privatePost := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.private.ID, "private realm title sentinel")
	for range 3 {
		if _, e := policy.ProcessDue(context.Background(), 100); e != nil {
			t.Fatal(e)
		}
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'post_id'=$2`, f.outside, privatePost.ID); n != 0 {
		t.Fatal("private realm leaked", n)
	}
	publicPost := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.public.ID, "public realm exact content")
	for range 3 {
		if _, e := policy.ProcessDue(context.Background(), 100); e != nil {
			t.Fatal(e)
		}
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'post_id'=$2`, f.outside, publicPost.ID); n != 1 {
		t.Fatal("immediate timestamp/public realm projection", n)
	}
}

func TestPGPublisherContentReadyRejectsMisplacedSchemaObjects(t *testing.T) {
	f := newBackendFixture(t)
	sink := notificationpg.New(f.db, nil, notifications.DefaultApp)
	repo := publishernotificationpg.New(f.db, socialpg.New(f.db), sink, notifications.DefaultApp)
	if e := repo.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DO $$BEGIN IF to_regclass('public.owned_publisher_content_notification_due_invalid') IS NOT NULL THEN ALTER INDEX owned_publisher_content_notification_due_invalid RENAME TO owned_publisher_content_notification_due; END IF; END$$`)
		_, _ = f.db.Exec(`ALTER TABLE owned_publisher_content_notification_intents ENABLE TRIGGER owned_publisher_content_intent_immutable`)
		_, _ = f.db.Exec(`ALTER TABLE owned_publisher_content_notification_receipts DROP CONSTRAINT IF EXISTS owned_publisher_content_intent_post`)
		_, _ = f.db.Exec(`ALTER TABLE owned_publisher_content_notification_intents ADD CONSTRAINT owned_publisher_content_intent_post FOREIGN KEY(post_id) REFERENCES owned_text_posts(id)`)
	})
	query(t, f.db, `ALTER INDEX owned_publisher_content_notification_due RENAME TO owned_publisher_content_notification_due_invalid`)
	if e := repo.Ready(context.Background()); e == nil {
		t.Fatal("missing exact index accepted")
	}
	query(t, f.db, `ALTER INDEX owned_publisher_content_notification_due_invalid RENAME TO owned_publisher_content_notification_due`)
	query(t, f.db, `ALTER TABLE owned_publisher_content_notification_intents DISABLE TRIGGER owned_publisher_content_intent_immutable`)
	if e := repo.Ready(context.Background()); e == nil {
		t.Fatal("disabled immutable trigger accepted")
	}
	query(t, f.db, `ALTER TABLE owned_publisher_content_notification_intents ENABLE TRIGGER owned_publisher_content_intent_immutable`)
	query(t, f.db, `ALTER TABLE owned_publisher_content_notification_intents DROP CONSTRAINT owned_publisher_content_intent_post`)
	query(t, f.db, `ALTER TABLE owned_publisher_content_notification_receipts ADD CONSTRAINT owned_publisher_content_intent_post CHECK(outcome IN('enqueued','suppressed'))`)
	if e := repo.Ready(context.Background()); e == nil {
		t.Fatal("same-named constraint on wrong relation accepted")
	}
	query(t, f.db, `ALTER TABLE owned_publisher_content_notification_receipts DROP CONSTRAINT owned_publisher_content_intent_post`)
	query(t, f.db, `ALTER TABLE owned_publisher_content_notification_intents ADD CONSTRAINT owned_publisher_content_intent_post FOREIGN KEY(post_id) REFERENCES owned_text_posts(id)`)
}

func TestPGPublisherContentReservedPrefixFailsClosedWithoutPolicy(t *testing.T) {
	f := newNotificationFixture(t)
	command := notificationCommand(f, "publisher-reserved")
	command.Key = "publisher-content:" + authorityUUID(t)
	command.External = true
	id := notificationEnqueue(t, f, command)
	if n, e := f.repo.ProcessPending(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE id=$1`, id); n != 0 {
		t.Fatal("reserved inbox admitted without policy", n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.deliveries WHERE notification_id=$1`, id); n != 0 {
		t.Fatal("reserved delivery admitted without policy", n)
	}
}
