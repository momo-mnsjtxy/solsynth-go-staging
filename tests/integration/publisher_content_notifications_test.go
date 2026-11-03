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

	// Publication watcher work uses the same durable hook for an immediate
	// reply and a due scheduled reply, with no notification before publication.
	backendCall(t, f.app, "POST", "/sphere/posts/"+post.ID+"/bookmark", f.otherToken, "", 200)
	repo, readCtx := publisherNotificationReadRepo(t, f)
	watcherCount := func(source string) int {
		for range 100 {
			n, e := repo.ProcessPending(context.Background(), 100)
			if e != nil {
				t.Fatal(e)
			}
			if n == 0 {
				break
			}
		}
		return subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND meta->>'post_id'=$2 AND meta->>'notification_type'='post_watch'`, f.other, source)
	}
	compose := func(relation string, scheduled bool, visibility int) posts.Post {
		t.Helper()
		timeField := ""
		if scheduled {
			timeField = fmt.Sprintf(`,"published_at":%q`, due.Format(time.RFC3339Nano))
		}
		return backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+p.ID, f.token, fmt.Sprintf(`{"content":"synthetic watcher publication","visibility":%d,%q:%q%s}`, visibility, relation, post.ID, timeField), 200))
	}
	immediate := compose("replied_post_id", false, posts.Public)
	drainPublisherContent(t, f.app)
	if n := watcherCount(immediate.ID); n != 1 {
		t.Fatal("ordinary reply watcher notification", n)
	}
	scheduled := compose("replied_post_id", true, posts.Public)
	drainPublisherContent(t, f.app)
	if n := watcherCount(scheduled.ID); n != 0 {
		t.Fatal("early scheduled reply watcher notification", n)
	}
	query(t, f.db, `UPDATE owned_post_schedules SET due_at=clock_timestamp() WHERE post_id=$1`, scheduled.ID)
	if n, e := f.app.ProcessScheduledPosts(context.Background(), 100); e != nil || n != 1 {
		t.Fatal("scheduled reply publication", n, e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	second := backendApp(t, f.db, f.config)
	for _, worker := range []*application.Backend{f.app, second} {
		wg.Add(1)
		go func(worker *application.Backend) {
			defer wg.Done()
			_, e := worker.ProcessPublisherContentNotifications(context.Background(), 100)
			errs <- e
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
	if n := watcherCount(scheduled.ID); n != 1 {
		t.Fatal("scheduled reply watcher duplicate or missing", n)
	}
	sender := &publisherNotificationSender{db: f.db, account: f.other}
	dispatchWatchers := func() {
		t.Helper()
		for range 100 {
			progressed, e := repo.DispatchOne(context.Background(), sender)
			if e != nil {
				t.Fatal(e)
			}
			if !progressed {
				return
			}
		}
		t.Fatal("watcher provider queue did not become idle")
	}
	sentFor := func(source string) int {
		count := 0
		for _, delivery := range sender.items {
			var meta map[string]any
			if e := json.Unmarshal(delivery.Notification.Meta, &meta); e != nil {
				t.Fatal(e)
			}
			if meta["post_id"] == source && meta["notification_type"] == "post_watch" {
				count++
			}
		}
		return count
	}
	dispatchWatchers()
	dispatchWatchers()
	if n := sentFor(scheduled.ID); n != 1 {
		t.Fatal("scheduled watcher external delivery missing or duplicated", n)
	}
	if n := sentFor(immediate.ID); n != 1 {
		t.Fatal("ordinary watcher external delivery missing or duplicated", n)
	}
	chain := compose("chained_post_id", false, posts.Public)
	drainPublisherContent(t, f.app)
	if n := watcherCount(chain.ID); n != 1 {
		t.Fatal("chain watcher notification", n)
	}

	revokedPush := compose("replied_post_id", false, posts.Public)
	drainPublisherContent(t, f.app)
	if n := watcherCount(revokedPush.ID); n != 1 {
		t.Fatal("watcher provider revocation positive control", n)
	}
	queued := compose("replied_post_id", false, posts.Public)
	backendCall(t, f.app, "PUT", "/sphere/posts/watch", f.otherToken, `{"bookmark":{"replies":false}}`, 200)
	drainPublisherContent(t, f.app)
	if n := watcherCount(queued.ID); n != 0 {
		t.Fatal("queued watcher ignored changed preference", n)
	}
	dispatchWatchers()
	if n := sentFor(revokedPush.ID); n != 0 {
		t.Fatal("watcher provider ignored preference revoked after projection", n)
	}
	page, e := repo.Replay(readCtx, f.other, notifications.DefaultApp, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), scheduled.ID) || strings.Contains(string(raw), immediate.ID) {
		t.Fatal("replay exposed reply notifications after preference revocation")
	}
	backendCall(t, f.app, "PUT", "/sphere/posts/watch", f.otherToken, `{"bookmark":{"replies":true}}`, 200)

	removed := compose("replied_post_id", false, posts.Public)
	backendCall(t, f.app, "DELETE", "/sphere/posts/"+post.ID+"/bookmark", f.otherToken, "", 204)
	backendCall(t, f.app, "POST", "/sphere/posts/"+post.ID+"/bookmark", f.otherToken, "", 200)
	drainPublisherContent(t, f.app)
	if n := watcherCount(removed.ID); n != 0 {
		t.Fatal("replaced bookmark revived an old watcher intent", n)
	}
	privateReply := compose("replied_post_id", false, posts.Private)
	drainPublisherContent(t, f.app)
	if n := watcherCount(privateReply.ID); n != 0 {
		t.Fatal("public-parent watcher saw a private reply", n)
	}
	mutedReply := compose("replied_post_id", false, posts.Public)
	// Effective relationships are a read-only expiry projection. Exercise the
	// canonical authenticated writer when changing the notification audience.
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/mute", f.token, `{}`, 200)
	drainPublisherContent(t, f.app)
	if n := watcherCount(mutedReply.ID); n != 0 {
		t.Fatal("publisher-muted recipient received watcher notification", n)
	}
	backendCall(t, f.app, "DELETE", "/stargate/relationships/"+f.other, f.token, "", 204)
	visible := compose("replied_post_id", false, posts.Public)
	drainPublisherContent(t, f.app)
	if n := watcherCount(visible.ID); n != 1 {
		t.Fatal("public reply watcher positive control", n)
	}
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+visible.ID, f.token, `{"visibility":3}`, 200)
	list, _, e := repo.List(readCtx, f.other, notifications.DefaultApp, 100, 0)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(list)
	if strings.Contains(string(raw), visible.ID) {
		t.Fatal("inbox exposed a withdrawn watcher source")
	}
	t.Run("large chain publication and current watcher membership", func(t *testing.T) {
		exerciseLongChainPublicationWatchers(t, f, p)
	})
	t.Run("publication recipient foreign key does not deadlock watcher", func(t *testing.T) {
		f := newBackendFixture(t)
		// Force the formerly unsafe order: the worker observes the smaller
		// recipient first while a publication already owns its author's row.
		if f.other > f.actor {
			f.actor, f.other = f.other, f.actor
			f.token, f.otherToken = f.otherToken, f.token
		}
		p := subscriptionPublisher(t, f)
		head := contentNotificationPost(t, f, p, posts.Public, "synthetic watch lock head")
		backendCall(t, f.app, "POST", "/sphere/posts/"+head.ID+"/bookmark", f.otherToken, "", 200)
		compose := func() posts.Post {
			t.Helper()
			return backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+p.ID, f.token, fmt.Sprintf(`{"content":"synthetic watch lock reply","visibility":0,"replied_post_id":%q}`, head.ID), 200))
		}
		first := compose()
		// Complete ordinary publisher discovery without claiming the pending
		// watcher. This fixture has no subscriber, so each source costs one step.
		for range 100 {
			pending := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_content_notification_intents WHERE completed_at IS NULL`)
			if pending == 0 {
				break
			}
			if n, e := f.app.ProcessPublisherContentNotifications(context.Background(), min(pending, 100)); e != nil || n == 0 {
				t.Fatal("source discovery did not advance", n, e)
			}
		}
		if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_content_notification_intents WHERE completed_at IS NULL`); n != 0 {
			t.Fatal("ordinary source discovery still pending", n)
		}
		pendingWatch := func(source string) int {
			return subscriptionCount(t, f.db, `SELECT count(*) FROM owned_post_watch_notification_intents q JOIN owned_publisher_content_notification_intents i ON i.id=q.publication_id WHERE i.source_id=$1 AND q.account_id=$2 AND q.processed_at IS NULL AND NOT q.suppressed AND q.notification_id IS NULL`, source, f.other)
		}
		if n := pendingWatch(first.ID); n != 1 {
			t.Fatal("missing pending watcher positive control", n)
		}
		notificationsRepo, _ := publisherNotificationReadRepo(t, f)
		publication := publishernotificationpg.New(f.db, socialpg.New(f.db), notificationsRepo, notifications.DefaultApp)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, e := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, `SET LOCAL lock_timeout='250ms'`); e != nil {
			t.Fatal(e)
		}
		// Prepare a second publication in that same transaction. Its real
		// RecordPublication hook below must still obtain the recipient FK's
		// KEY SHARE lock after the watch worker has tried the opposite order.
		at := time.Now().UTC().Truncate(time.Microsecond)
		second := posts.Post{ID: authorityUUID(t), PublisherID: p.ID, Publisher: p, Content: "synthetic watch lock concurrent publication", Visibility: posts.Public, CreatedAt: at, UpdatedAt: at, PublishedAt: &at}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_text_posts(id,publisher_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,$3,0,0,$4,$4,$4)`, second.ID, p.ID, second.Content, at); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_relations(post_id,replied_post_id)VALUES($1,$2)`, second.ID, head.ID); e != nil {
			t.Fatal(e)
		}
		workerCtx, workerCancel := context.WithTimeout(context.Background(), time.Second)
		n, workerErr := f.app.ProcessPublisherContentNotifications(workerCtx, 100)
		workerCancel()
		if workerErr != nil || n != 0 {
			t.Fatal("contended watcher must release its partial lock plan without claiming work", n, workerErr)
		}
		if n := pendingWatch(first.ID); n != 1 {
			t.Fatal("contention changed or suppressed pending intent", n)
		}
		if e = publication.RecordPublication(ctx, f.db, tx, second); e != nil {
			t.Fatal("publication recipient FK waited or deadlocked after worker contention", e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal("contended publication could not commit", e)
		}
		backendCall(t, f.app, "GET", "/sphere/posts/"+second.ID, f.token, "", 200)
		third := compose()
		drainPublisherContent(t, f.app)
		drainPublisherContent(t, f.app)
		for range 100 {
			n, e := notificationsRepo.ProcessPending(context.Background(), 100)
			if e != nil {
				t.Fatal(e)
			}
			if n == 0 {
				break
			}
		}
		for _, source := range []string{first.ID, second.ID, third.ID} {
			if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND meta->>'post_id'=$2 AND meta->>'notification_type'='post_watch'`, f.other, source); n != 1 {
				t.Fatal("retry lost or duplicated watcher delivery", source, n)
			}
		}
		sender := &publisherNotificationSender{db: f.db, account: f.other}
		for range 2 {
			for range 100 {
				progressed, e := notificationsRepo.DispatchOne(context.Background(), sender)
				if e != nil {
					t.Fatal(e)
				}
				if !progressed {
					break
				}
			}
		}
		for _, source := range []string{first.ID, second.ID, third.ID} {
			count := 0
			for _, delivery := range sender.items {
				var meta map[string]any
				if e := json.Unmarshal(delivery.Notification.Meta, &meta); e != nil {
					t.Fatal(e)
				}
				if meta["post_id"] == source && meta["notification_type"] == "post_watch" {
					count++
				}
			}
			if count != 1 {
				t.Fatal("retry lost or duplicated external watcher delivery", source, count)
			}
		}
	})
}

// Long chains are ordinary content: bounded resolver queries must batch their
// inputs without introducing a new publication limit. Witness-specific targets
// retain the same visibility, identity and current chain-membership fences.
func exerciseLongChainPublicationWatchers(t *testing.T, f backendFixture, p accounts.Publisher) {
	t.Helper()
	head := contentNotificationPost(t, f, p, posts.Public, "synthetic long chain head")
	query(t, f.db, `WITH added AS(INSERT INTO owned_text_posts(id,publisher_id,content,visibility,type,published_at,created_at,updated_at)SELECT gen_random_uuid(),$1::uuid,'synthetic long chain member',0,0,clock_timestamp(),clock_timestamp(),clock_timestamp()FROM generate_series(1,103)RETURNING id)INSERT INTO owned_post_relations(post_id,chained_post_id)SELECT id,$2::uuid FROM added`, p.ID, head.ID)
	var last string
	if e := f.db.QueryRow(`SELECT p.id::text FROM owned_text_posts p JOIN owned_post_relations l ON l.post_id=p.id WHERE l.chained_post_id=$1 ORDER BY p.id DESC LIMIT 1`, head.ID).Scan(&last); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", "/sphere/posts/"+last+"/bookmark", f.otherToken, "", 200)
	compose := func() posts.Post {
		t.Helper()
		return backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+p.ID, f.token, fmt.Sprintf(`{"content":"synthetic long chain publication","visibility":0,"chained_post_id":%q}`, head.ID), 200))
	}
	watchCount := func(source string) int {
		return subscriptionCount(t, f.db, `SELECT count(*)FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'post_id'=$2 AND command->'Meta'->>'notification_type'='post_watch'`, f.other, source)
	}
	lateBatch := compose()
	drainPublisherContent(t, f.app)
	if got := watchCount(lateBatch.ID); got != 1 {
		t.Fatal("watcher after the first 100 chain posts was lost", got)
	}
	var targetCount int
	if e := f.db.QueryRow(`SELECT cardinality(q.targets)FROM owned_post_watch_notification_intents q JOIN owned_publisher_content_notification_intents i ON i.id=q.publication_id WHERE i.source_id=$1 AND q.account_id=$2 AND q.event='chains'`, lateBatch.ID, f.other).Scan(&targetCount); e != nil || targetCount > 2 {
		t.Fatal("recipient copied unrelated chain dependencies", targetCount, e)
	}
	// A queued chain notification cannot survive its only witness moving out of
	// the chain, even if that post and its original bookmark remain readable.
	moved := compose()
	query(t, f.db, `UPDATE owned_post_relations SET chained_post_id=NULL WHERE post_id=$1`, last)
	drainPublisherContent(t, f.app)
	if got := watchCount(moved.ID); got != 0 {
		t.Fatal("detached witness revived chain notification", got)
	}
	query(t, f.db, `UPDATE owned_post_relations SET chained_post_id=$2 WHERE post_id=$1`, last, head.ID)
	// One account may legitimately watch every post in a long chain. Its own
	// dependency set can exceed 101 and must fit the persisted intent too.
	query(t, f.db, `INSERT INTO owned_post_bookmarks(id,post_id,account_id,created_at)SELECT gen_random_uuid(),p.id,$2::uuid,clock_timestamp()FROM owned_text_posts p WHERE p.id=$1 OR EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.chained_post_id=$1)ON CONFLICT(post_id,account_id)DO NOTHING`, head.ID, f.other)
	allPosts := compose()
	if e := f.db.QueryRow(`SELECT cardinality(q.targets)FROM owned_post_watch_notification_intents q JOIN owned_publisher_content_notification_intents i ON i.id=q.publication_id WHERE i.source_id=$1 AND q.account_id=$2 AND q.event='chains'`, allPosts.ID, f.other).Scan(&targetCount); e != nil || targetCount <= 101 {
		t.Fatal("large legitimate watcher set was rejected or truncated", targetCount, e)
	}
	drainPublisherContent(t, f.app)
	if got := watchCount(allPosts.ID); got != 1 {
		t.Fatal("large witness set duplicated or lost publication", got)
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
