// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

func subscriptionPublisher(t *testing.T, f backendFixture) accounts.Publisher {
	t.Helper()
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,10,now()) ON CONFLICT(account_id)DO UPDATE SET total=10`, f.actor)
	return backendDecode[accounts.Publisher](t, backendCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Subscription fixture"}`, "sub_"+strings.ReplaceAll(authorityUUID(t), "-", "")), 200))
}
func subscriptionCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if e := db.QueryRow(q, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func subscriptionPublicPost(t *testing.T, f backendFixture, p accounts.Publisher, at time.Time) posts.Post {
	t.Helper()
	v := backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+p.ID, f.token, `{"content":"subscription visible content","visibility":0}`, 200))
	query(t, f.db, `UPDATE owned_text_posts SET published_at=$2 WHERE id=$1`, v.ID, at)
	return v
}
func subscriptionStream(t *testing.T, db *sql.DB, pub string, visibility, status int, at time.Time) string {
	t.Helper()
	id := authorityUUID(t)
	state := "ready"
	if status == live.Active {
		state = "active"
	}
	query(t, db, `INSERT INTO owned_livestreams(id,publisher_id,title,type,visibility,status,room_name,operation_state,started_at,created_at,updated_at)VALUES($1,$2,'Synthetic stream',0,$3,$4,$5,$6,$7,$7,$7)`, id, pub, visibility, status, "synthetic_"+id, state, at)
	return id
}
func TestPGPublisherSubscriptionAdministrationAndInbox(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	root := "/sphere/publishers/" + pub.Name
	backendCall(t, f.app, "GET", root+"/subscribers", f.otherToken, "", 403)
	backendCall(t, f.app, "POST", root+"/subscribers/"+f.actor, f.otherToken, "", 403)
	added := backendDecode[timeline.Subscriber](t, backendCall(t, f.app, "POST", root+"/subscribers/"+f.other, f.token, "", 200))
	if added.Subscription.AccountID != f.other || added.Subscription.Notify || !added.Subscription.IsActive || added.Subscription.Publisher == nil || added.Account == nil || added.Account.ID != f.other {
		t.Fatal(added)
	}
	backendCall(t, f.app, "POST", root+"/subscribers/"+f.other, f.token, "", 400)
	w := backendCall(t, f.app, "GET", root+"/subscribers?offset=0&take=1", f.token, "", 200)
	rows := backendDecode[[]timeline.Subscriber](t, w)
	if w.Header().Get("X-Total") != "1" || len(rows) != 1 || rows[0].Subscription.ID != added.Subscription.ID {
		t.Fatal(w.Header(), rows)
	}
	backendCall(t, f.app, "GET", root+"/subscription/read-status", f.token, "", 404)
	backendCall(t, f.app, "PATCH", root+"/subscription/me/notify", f.otherToken, `{"notify":true}`, 200)
	backendCall(t, f.app, "PATCH", root+"/subscription/me/notify", f.otherToken, `{"notify":true}`, 200)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND source_key=$2`, f.other, "publisher-subscriber-added:"+added.Subscription.ID); n != 1 {
		t.Fatal("duplicate durable notification", n)
	}
	var raw []byte
	if e := f.db.QueryRow(`SELECT command FROM notification_v1.outbox WHERE account_id=$1 AND source_key=$2`, f.other, "publisher-subscriber-added:"+added.Subscription.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var command notifications.Command
	if e := json.Unmarshal(raw, &command); e != nil || command.External || command.Topic != "subscriber_added" {
		t.Fatal("external delivery invented", command, e)
	}
	inbox := notificationpg.New(f.db, nil, "dev.solsynth.solian")
	for {
		n, e := inbox.ProcessPending(context.Background(), 100)
		if e != nil {
			t.Fatal(e)
		}
		if n < 100 {
			break
		}
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1 AND source_key=$2`, f.other, command.Key); n != 1 {
		t.Fatal("missing canonical inbox", n)
	}
	backendCall(t, f.app, "DELETE", root+"/subscribers/"+f.other, f.otherToken, "", 403)
	backendCall(t, f.app, "DELETE", root+"/subscribers/"+f.other, f.token, "", 204)
	backendCall(t, f.app, "DELETE", root+"/subscribers/"+f.other, f.token, "", 404)
	backendCall(t, f.app, "GET", root+"/subscription/read-status", f.otherToken, "", 404)
	backendCall(t, f.app, "POST", root+"/subscribers/"+f.other, f.token, "", 400)
	// A user's own resume follows the authored self-subscribe service policy.
	backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", root+"/unsubscribe", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", root+"/subscribers/"+f.other, f.token, "", 400)
}
func TestPGPublisherSubscriptionReadReceiptsAndVisibleContent(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	root := "/sphere/publishers/" + pub.Name
	backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	p := subscriptionPublicPost(t, f, pub, at)
	for _, body := range []string{`{"content":"private","visibility":3}`, `{"content":"draft","visibility":0,"drafted_at":"2025-01-01T00:00:00Z"}`, fmt.Sprintf(`{"content":"reply","visibility":0,"replied_post_id":%q}`, p.ID)} {
		backendCall(t, f.app, "POST", "/sphere/posts?pub="+pub.ID, f.token, body, 200)
	}
	initial := backendDecode[timeline.SubscriptionReadStatus](t, backendCall(t, f.app, "GET", root+"/subscription/read-status", f.otherToken, "", 200))
	if initial.LatestContentAt == nil || !initial.LatestContentAt.Equal(at) || !initial.HasNewContent || initial.Subscription.LastReadAt != nil {
		t.Fatal("content boundary", initial)
	}
	body := fmt.Sprintf(`{"last_read_at":%q}`, at.Add(321*time.Nanosecond).Format(time.RFC3339Nano))
	v := backendDecode[timeline.SubscriptionReadStatus](t, backendCall(t, f.app, "PUT", root+"/subscription/read-status", f.otherToken, body, 200))
	if v.HasNewContent || v.Subscription.LastReadAt == nil || !v.Subscription.LastReadAt.Equal(at) {
		t.Fatal(v)
	}
	backendCall(t, f.app, "PUT", root+"/subscription/read-status", f.otherToken, body, 200)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_timeline_audit WHERE actor_id=$1 AND target_id=$2 AND action='publisher.subscription.read'`, f.other, pub.ID); n != 1 {
		t.Fatal("receipt replay changed audit", n)
	}
	list := backendDecode[[]timeline.SubscriptionItem](t, backendCall(t, f.app, "GET", "/sphere/publishers/subscriptions", f.otherToken, "", 200))
	if len(list) != 1 || list[0].HasNewContent || list[0].LastReadAt == nil || !list[0].LastReadAt.Equal(at) {
		t.Fatal("list ignored receipt", list)
	}
	query(t, f.db, `UPDATE owned_text_posts SET published_at=$2 WHERE id=$1`, p.ID, at.Add(time.Minute))
	marked := backendDecode[map[string]int](t, backendCall(t, f.app, "PUT", "/sphere/publishers/subscriptions/read-status", f.otherToken, "", 200))
	if marked["updated_count"] != 1 {
		t.Fatal(marked)
	}
	marked = backendDecode[map[string]int](t, backendCall(t, f.app, "PUT", "/sphere/publishers/subscriptions/read-status", f.otherToken, "", 200))
	if marked["updated_count"] != 0 {
		t.Fatal("read-all replay", marked)
	}
	other := subscriptionPublisher(t, f)
	backendCall(t, f.app, "POST", "/sphere/publishers/"+other.Name+"/subscribe", f.otherToken, "", 200)
	marked = backendDecode[map[string]int](t, backendCall(t, f.app, "PUT", "/sphere/publishers/subscriptions/read-status", f.otherToken, "", 200))
	if marked["updated_count"] != 0 {
		t.Fatal("empty publisher marked", marked)
	}
}
func TestPGPublisherSubscriptionLiveProjectionAndBlocks(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	other := subscriptionPublisher(t, f)
	root := "/sphere/publishers/" + pub.Name
	backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	active := subscriptionStream(t, f.db, pub.ID, live.Public, live.Active, at)
	subscriptionStream(t, f.db, pub.ID, live.Private, live.Active, at.Add(10*time.Minute))
	subscriptionStream(t, f.db, pub.ID, live.Unlisted, live.Active, at.Add(20*time.Minute))
	subscriptionStream(t, f.db, other.ID, live.Public, live.Active, at.Add(30*time.Minute))
	endedAt := at.Add(time.Minute)
	subscriptionStream(t, f.db, pub.ID, live.Public, live.Ended, endedAt)
	streams := backendDecode[[]live.Stream](t, backendCall(t, f.app, "GET", "/sphere/publishers/subscriptions/live", f.otherToken, "", 200))
	if len(streams) != 1 || streams[0].ID != active || streams[0].Publisher == nil || streams[0].IngressStreamKey != nil {
		t.Fatal("live visibility", streams)
	}
	status := backendDecode[timeline.SubscriptionReadStatus](t, backendCall(t, f.app, "GET", root+"/subscription/read-status", f.otherToken, "", 200))
	if status.LatestContentAt == nil || !status.LatestContentAt.Equal(endedAt) {
		t.Fatal("public ended stream missing/new private timestamp leaked", status)
	}
	list := backendDecode[[]timeline.SubscriptionItem](t, backendCall(t, f.app, "GET", "/sphere/publishers/subscriptions", f.otherToken, "", 200))
	if len(list) != 1 || !list[0].IsLive {
		t.Fatal(list)
	}
	// Canonical owner punishments apply to every subscription projection, just
	// as they do to public account hydration and the ordinary stream reader.
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()+interval '1 hour')`, f.actor)
	for _, eligible := range []bool{true, false, true} {
		if !eligible {
			query(t, f.db, `UPDATE owned_authority_punishments SET starts_at=clock_timestamp()-interval '1 minute' WHERE account_id=$1 AND kind='disable_account'`, f.actor)
		}
		list = backendDecode[[]timeline.SubscriptionItem](t, backendCall(t, f.app, "GET", "/sphere/publishers/subscriptions", f.otherToken, "", 200))
		streams = backendDecode[[]live.Stream](t, backendCall(t, f.app, "GET", "/sphere/publishers/subscriptions/live", f.otherToken, "", 200))
		if eligible {
			if len(list) != 1 || !list[0].IsLive || list[0].LatestContentAt == nil || !list[0].LatestContentAt.Equal(endedAt) || len(streams) != 1 {
				t.Fatal("future or expired punishment hid content", list, streams)
			}
			continue
		}
		if len(list) != 0 || len(streams) != 0 {
			t.Fatal("punished owner exposed subscription content", list, streams)
		}
		backendCall(t, f.app, "GET", root+"/subscription/read-status", f.otherToken, "", 404)
		marked := backendDecode[map[string]int](t, backendCall(t, f.app, "PUT", "/sphere/publishers/subscriptions/read-status", f.otherToken, "", 200))
		if marked["updated_count"] != 0 || subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE account_id=$1 AND last_read_at IS NOT NULL`, f.other) != 0 {
			t.Fatal("punished owner advanced receipt", marked)
		}
		query(t, f.db, `UPDATE owned_authority_punishments SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1 AND kind='disable_account'`, f.actor)
	}
	if _, e := socialpg.New(f.db).ChangeRelationship(context.Background(), f.other, f.actor, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", root+"/subscription/read-status", f.otherToken, "", 404)
	streams = backendDecode[[]live.Stream](t, backendCall(t, f.app, "GET", "/sphere/publishers/subscriptions/live", f.otherToken, "", 200))
	if len(streams) != 0 {
		t.Fatal("blocked stream exposed", streams)
	}
	list = backendDecode[[]timeline.SubscriptionItem](t, backendCall(t, f.app, "GET", "/sphere/publishers/subscriptions", f.otherToken, "", 200))
	if len(list) != 0 {
		t.Fatal("blocked subscriptions exposed", list)
	}
}
func TestPGPublisherSubscriptionStrictRoutesAndAuthority(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	root := "/api/publishers/" + pub.Name
	backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200)
	for _, body := range []string{`{"notify":null}`, `{"notify":true,"Notify":false}`, `{"notify":false,"account_id":"x"}`, `null`} {
		backendCall(t, f.app, "PATCH", root+"/subscription/me/notify", f.otherToken, body, 400)
	}
	for _, body := range []string{`{"last_read_at":"bad"}`, `{"last_read_at":null,"Last_Read_At":null}`, `{"account_id":"x"}`, `[]`} {
		backendCall(t, f.app, "PUT", root+"/subscription/read-status", f.otherToken, body, 400)
	}
	backendCall(t, f.app, "GET", root+"/subscribers?offset=-1", f.token, "", 400)
	backendCall(t, f.app, "GET", root+"/subscribers?take=1&take=2", f.token, "", 400)
	backendCall(t, f.app, "GET", root+"/subscription/read-status?account_id="+f.actor, f.otherToken, "", 501)
	backendCall(t, f.app, "GET", root+"/subscription/read-status", "", "", 401)
	req := httptest.NewRequest("PUT", root+"/subscription/read-status", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Account-ID", f.other)
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("raw identity trusted", w.Code)
	}
	pair, e := f.issuer.IssueSession(context.Background(), f.other, authority.SessionOptions{Scopes: []string{"posts.read"}})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "PATCH", root+"/subscription/me/notify", pair.Token, `{"notify":false}`, 403)
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.other, []authority.Rule{{Pattern: "publishers.subscriptions.manage", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "PUT", root+"/subscription/read-status", f.otherToken, `{}`, 403)
}
func TestPGPublisherSubscriptionAuditRollbackAndConcurrentAdd(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	root := "/sphere/publishers/" + pub.Name
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT IF EXISTS synthetic_subscription_rollback`)
	})
	query(t, f.db, `ALTER TABLE owned_timeline_audit ADD CONSTRAINT synthetic_subscription_rollback CHECK(actor_id<>'`+f.actor+`'::uuid OR action<>'publisher.subscriber.add') NOT VALID`)
	backendCall(t, f.app, "POST", root+"/subscribers/"+f.other, f.token, "", 503)
	query(t, f.db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT synthetic_subscription_rollback`)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE publisher_id=$1`, pub.ID); n != 0 {
		t.Fatal("follow survived failed audit", n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'publisher_id'=$2`, f.other, pub.ID); n != 0 {
		t.Fatal("outbox survived rollback", n)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := fixtureNoBodyRequest("POST", root+"/subscribers/"+f.other, f.token)
			w := httptest.NewRecorder()
			f.app.ServeHTTP(w, req)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for n := range codes {
		counts[n]++
	}
	if counts[200] != 1 || counts[400] != 1 {
		t.Fatal("concurrent add", counts)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE publisher_id=$1`, pub.ID); n != 1 {
		t.Fatal(n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND command->'Meta'->>'publisher_id'=$2`, f.other, pub.ID); n != 1 {
		t.Fatal("notification dedup", n)
	}
	subscriptionPublicPost(t, f, pub, time.Now().UTC().Add(-time.Minute))
	query(t, f.db, `ALTER TABLE owned_timeline_audit ADD CONSTRAINT synthetic_subscription_read_rollback CHECK(actor_id<>'`+f.other+`'::uuid OR action<>'publisher.subscriptions.read_all') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT IF EXISTS synthetic_subscription_read_rollback`)
	})
	backendCall(t, f.app, "PUT", "/sphere/publishers/subscriptions/read-status", f.otherToken, "", 503)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE account_id=$1 AND last_read_at IS NOT NULL`, f.other); n != 0 {
		t.Fatal("read-all survived failed audit", n)
	}
	query(t, f.db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT synthetic_subscription_read_rollback`)

}
func subscriptionBlockWait(t *testing.T, ctx context.Context, db *sql.DB, blocker int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("no observed lock wait", ctx.Err())
		case <-ticker.C:
		}
	}
}
func TestPGPublisherSubscriptionWaitRechecksManagerAndSession(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	root := "/sphere/publishers/" + pub.Name
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
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, pub.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, fixtureNoBodyRequest("GET", root+"/subscribers", f.token))
		done <- w.Code
	}()
	subscriptionBlockWait(t, ctx, f.db, pid)
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_publisher_members SET role=50 WHERE publisher_id=$1 AND account_id=$2`, pub.ID, f.actor); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if code := <-done; code != 403 {
		t.Fatal("stale manager read", code)
	}
	backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200)
	subscriptionStream(t, f.db, pub.ID, live.Public, live.Active, time.Now().UTC().Add(-time.Minute))
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()+interval '1 hour')`, f.actor)
	blocker, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, pub.ID); e != nil {
		t.Fatal(e)
	}
	bulkDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, fixtureNoBodyRequest("PUT", "/sphere/publishers/subscriptions/read-status", f.otherToken))
		bulkDone <- w
	}()
	subscriptionBlockWait(t, ctx, f.db, pid)
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_punishments SET starts_at=clock_timestamp()-interval '1 second' WHERE account_id=$1 AND kind='disable_account'`, f.actor); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	bulk := <-bulkDone
	if bulk.Code != 200 || backendDecode[map[string]int](t, bulk)["updated_count"] != 0 {
		t.Fatal("owner punishment after publisher wait advanced bulk receipt", bulk.Code, bulk.Body.String())
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE account_id=$1 AND last_read_at IS NOT NULL`, f.other); n != 0 {
		t.Fatal("hidden publisher bulk receipt persisted", n)
	}
	query(t, f.db, `UPDATE owned_authority_punishments SET expires_at=clock_timestamp()-interval '1 microsecond' WHERE account_id=$1 AND kind='disable_account'`, f.actor)
	blocker, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.other); e != nil {
		t.Fatal(e)
	}
	go func() {
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, fixtureRawJSONRequest("PUT", root+"/subscription/read-status", f.otherToken, `{}`))
		done <- w.Code
	}()
	subscriptionBlockWait(t, ctx, f.db, pid)
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=now() WHERE account_id=$1`, f.other); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if code := <-done; code != 401 {
		t.Fatal("stale session mutated receipt", code)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE account_id=$1 AND last_read_at IS NOT NULL`, f.other); n != 0 {
		t.Fatal("revoked receipt persisted", n)
	}
}
func TestPGPublisherSubscriptionRealmVisibilityAndMembership(t *testing.T) {
	f := newRealmPublishingFixture(t)
	f.timeline.WithSubscriptionStreams(livestreampg.NewSubscriptionReader(f.accounts, f.social, f.repo))
	if _, e := f.timeline.Subscribe(f.otherCtx, f.other, f.privatePub.ID); e != nil {
		t.Fatal(e)
	}
	f.post(t, f.privatePub, "realm subscription content", posts.Public)
	if _, e := f.timeline.SubscriptionReadStatus(f.otherCtx, f.other, f.privatePub.ID); e != nil {
		t.Fatal(e)
	}
	subscriptionStream(t, f.db, f.privatePub.ID, live.Public, live.Active, time.Now().UTC().Add(-time.Minute))
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()-interval '1 second')`, f.actor)
	if _, e := f.timeline.SubscriptionReadStatus(f.otherCtx, f.other, f.privatePub.ID); !errors.Is(e, timeline.ErrPublisherNotFound) {
		t.Fatal("punished realm owner exposed receipt", e)
	}
	if rows, total, e := f.timeline.Subscriptions(f.otherCtx, f.other, 0, 20); e != nil || len(rows) != 0 || total != 0 {
		t.Fatal("punished realm owner exposed subscription", rows, total, e)
	}
	if streams, e := f.timeline.SubscribedLiveStreams(f.otherCtx, f.other, 0, 20); e != nil || len(streams) != 0 {
		t.Fatal("punished realm owner exposed stream", streams, e)
	}
	if n, e := f.timeline.MarkAllSubscriptionsRead(f.otherCtx, f.other); e != nil || n != 0 {
		t.Fatal("punished realm owner advanced receipt", n, e)
	}
	query(t, f.db, `UPDATE owned_authority_punishments SET expires_at=clock_timestamp()-interval '1 microsecond' WHERE account_id=$1 AND kind='disable_account'`, f.actor)
	if _, _, e := f.timeline.Subscribers(f.outsideCtx, f.outside, f.privatePub.ID, 0, 20); !errors.Is(e, timeline.ErrPublisherNotFound) {
		t.Fatal("private subscriber roster", e)
	}
	if _, e := f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.timeline.UpdateSubscriptionReadStatus(f.otherCtx, f.other, f.privatePub.ID, nil); !errors.Is(e, timeline.ErrPublisherNotFound) {
		t.Fatal("departed member receipt", e)
	}
	if n, e := f.timeline.MarkAllSubscriptionsRead(f.otherCtx, f.other); e != nil || n != 0 {
		t.Fatal("hidden realm bulk receipt", n, e)
	}
	if rows, total, e := f.timeline.Subscriptions(f.otherCtx, f.other, 0, 20); e != nil || len(rows) != 0 || total != 0 {
		t.Fatal(rows, total, e)
	}
}
func TestPGPublisherSubscriptionSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "publisher-subscription-state.json"
	type saved struct {
		Actor, Other, Token, OtherToken, Publisher, Name, Subscription, Stream string
		ReadAt                                                                 time.Time
		Audit                                                                  int
		Authority                                                              restartAuthoritySnapshot
	}
	if phase == "before" {
		f := newBackendFixture(t)
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f.config.Now = func() time.Time { return time.Now().UTC() }
		var e error
		f.issuer, e = authority.New(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.token = pgIssue(t, f.issuer, f.actor).Token
		f.otherToken = pgIssue(t, f.issuer, f.other).Token
		f.app = backendApp(t, f.db, f.config)
		pub := subscriptionPublisher(t, f)
		root := "/sphere/publishers/" + pub.Name
		sub := backendDecode[timeline.Subscriber](t, backendCall(t, f.app, "POST", root+"/subscribers/"+f.other, f.token, "", 200))
		at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		stream := subscriptionStream(t, f.db, pub.ID, live.Public, live.Active, at)
		backendCall(t, f.app, "PUT", root+"/subscription/read-status", f.otherToken, fmt.Sprintf(`{"last_read_at":%q}`, at.Format(time.RFC3339Nano)), 200)
		v := saved{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Publisher: pub.ID, Name: pub.Name, Subscription: sub.Subscription.ID, Stream: stream, ReadAt: at, Audit: subscriptionCount(t, f.db, `SELECT count(*) FROM owned_timeline_audit WHERE target_id=$1`, pub.ID), Authority: saveAuthoritySnapshot(t, f.config)}
		saveRestartFixture(t, marker, v)
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	// Restore the original public verifier and elapsed wall clock only. No new
	// accounts, tokens, grants, subscription state or content fixtures after restart.
	app := backendApp(t, db, restoreAuthoritySnapshot(t, v.Authority))
	root := "/sphere/publishers/" + v.Name
	read := backendDecode[timeline.SubscriptionReadStatus](t, backendCall(t, app, "GET", root+"/subscription/read-status", v.OtherToken, "", 200))
	if read.Subscription.ID != v.Subscription || read.Subscription.Notify || read.Subscription.LastReadAt == nil || !read.Subscription.LastReadAt.Equal(v.ReadAt) || read.HasNewContent {
		t.Fatal("saved state changed", read)
	}
	backendCall(t, app, "PUT", root+"/subscription/read-status", v.OtherToken, fmt.Sprintf(`{"last_read_at":%q}`, v.ReadAt.Format(time.RFC3339Nano)), 200)
	backendCall(t, app, "PATCH", root+"/subscription/me/notify", v.OtherToken, `{"notify":false}`, 200)
	backendCall(t, app, "POST", root+"/subscribers/"+v.Other, v.Token, "", 400)
	streams := backendDecode[[]live.Stream](t, backendCall(t, app, "GET", "/sphere/publishers/subscriptions/live", v.OtherToken, "", 200))
	if len(streams) != 1 || streams[0].ID != v.Stream {
		t.Fatal("saved stream projection", streams)
	}
	if n := subscriptionCount(t, db, `SELECT count(*) FROM owned_timeline_audit WHERE target_id=$1`, v.Publisher); n != v.Audit {
		t.Fatal("restart replay wrote audit", n, v.Audit)
	}
	if n := subscriptionCount(t, db, `SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND source_key=$2`, v.Other, "publisher-subscriber-added:"+v.Subscription); n != 1 {
		t.Fatal("restart duplicated notification", n)
	}
}

func TestPGPublisherSubscriptionPendingAndTargetPrivacy(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	root := "/sphere/publishers/" + pub.Name
	query(t, f.db, `INSERT INTO owned_publisher_follow_policy(publisher_id,requires_approval)VALUES($1,true)`, pub.ID)
	pending := backendDecode[timeline.SubscriptionStatus](t, backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200))
	if !pending.IsPending || pending.Subscription == nil {
		t.Fatal(pending)
	}
	backendCall(t, f.app, "GET", root+"/subscription/read-status", f.otherToken, "", 404)
	backendCall(t, f.app, "PATCH", root+"/subscription/me/notify", f.otherToken, `{"notify":false}`, 404)
	backendCall(t, f.app, "POST", root+"/subscribers/"+f.other, f.token, "", 400)
	rows := backendDecode[[]timeline.Subscriber](t, backendCall(t, f.app, "GET", root+"/subscribers", f.token, "", 200))
	if len(rows) != 0 {
		t.Fatal("pending roster leak", rows)
	}
	backendCall(t, f.app, "POST", root+"/subscription/requests/"+pending.Subscription.ID+"/approve", f.token, "", 200)
	backendCall(t, f.app, "GET", root+"/subscription/read-status", f.otherToken, "", 200)
	other := subscriptionPublisher(t, f)
	backendCall(t, f.app, "DELETE", "/sphere/publishers/"+other.Name+"/subscribers/"+f.other, f.token, "", 404)
	if _, e := socialpg.New(f.db).ChangeRelationship(context.Background(), f.other, f.actor, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", "/sphere/publishers/"+other.Name+"/subscribers/"+f.other, f.token, "", 404)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	backendCall(t, f.app, "POST", "/sphere/publishers/"+other.Name+"/subscribers/"+f.other, f.token, "", 404)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE publisher_id=$1`, other.ID); n != 0 {
		t.Fatal("disabled target subscribed", n)
	}
}
func TestPGPublisherSubscriptionExpiryRollsBackAfterPublisherWait(t *testing.T) {
	f := newBackendFixture(t)
	pub := subscriptionPublisher(t, f)
	root := "/sphere/publishers/" + pub.Name
	backendCall(t, f.app, "POST", root+"/subscribe", f.otherToken, "", 200)
	var clock atomic.Int64
	clock.Store(f.config.Now().Unix())
	f.config.Now = func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	f.app = backendApp(t, f.db, f.config)
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
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, pub.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, fixtureRawJSONRequest("PUT", root+"/subscription/read-status", f.otherToken, `{}`))
		done <- w.Code
	}()
	subscriptionBlockWait(t, ctx, f.db, pid)
	clock.Add(int64((2 * time.Hour) / time.Second))
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if code := <-done; code != 401 {
		t.Fatal("proof expired during publisher wait", code)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_publisher_follows WHERE account_id=$1 AND last_read_at IS NOT NULL`, f.other); n != 0 {
		t.Fatal("expired receipt survived rollback", n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_timeline_audit WHERE actor_id=$1 AND action='publisher.subscription.read'`, f.other); n != 0 {
		t.Fatal("expired audit survived rollback", n)
	}
}
