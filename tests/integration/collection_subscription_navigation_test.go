// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type collectionNavigationFixture struct {
	backendFixture
	pub        accounts.Publisher
	collection posts.Collection
	root       string
}

func newCollectionNavigationFixture(t *testing.T) collectionNavigationFixture {
	t.Helper()
	f := collectionNavigationFixture{backendFixture: newBackendFixture(t)}
	f.pub = subscriptionPublisher(t, f.backendFixture)
	f.collection = backendDecode[posts.Collection](t, backendCall(t, f.app, "POST", "/sphere/publishers/"+f.pub.Name+"/collections", f.token, `{"slug":"series"}`, 201))
	f.root = "/sphere/publishers/" + f.pub.Name + "/collections/series"
	return f
}
func collectionNavigationPost(t *testing.T, f collectionNavigationFixture, visibility int, body string) posts.Post {
	t.Helper()
	return backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": body, "visibility": visibility}, 200))
}
func collectionNavigationAdd(t *testing.T, f collectionNavigationFixture, ids ...string) {
	t.Helper()
	backendJSONCall(t, f.app, "POST", f.root+"/posts/batch", f.token, map[string]any{"post_ids": ids}, 204)
}
func collectionNavigationFile(t *testing.T, db *sql.DB, owner, post string) string {
	t.Helper()
	id := authorityUUID(t)
	query(t, db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1,10000) ON CONFLICT DO NOTHING`, owner)
	query(t, db, `INSERT INTO owned_media_files(id,account_id,idempotency_key,name,mime_type,size,sha256,is_public,state,charge,expires_at,created_at,updated_at)VALUES($1::uuid,$2::uuid,$1::text,'synthetic-navigation-file','text/plain',0,repeat('0',64),false,'ready','used',clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp())`, id, owner)
	query(t, db, `INSERT INTO owned_post_media(post_id,file_id,position,owner_authorized,created_at)VALUES($1,$2,0,true,clock_timestamp())`, post, id)
	return id
}
func collectionAsync(ctx context.Context, h http.Handler, method, path, token string) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := fixtureNoBodyRequest(method, path, token).WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		done <- w
	}()
	return done
}
func TestPGCollectionSubscriptionAuthoredHTTPAndIncarnations(t *testing.T) {
	f := newCollectionNavigationFixture(t)
	backendCall(t, f.app, "POST", f.root+"/subscribe", "", "", 401)
	backendCall(t, f.app, "GET", f.root+"/subscription", f.otherToken, "", 404)
	backendCall(t, f.app, "POST", f.root+"/unsubscribe", f.otherToken, "", 204)
	first := backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 201))
	second := backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 200))
	if first.ID == "" || first.ID != second.ID || first.AccountID != f.other || first.CollectionID == nil || *first.CollectionID != f.collection.ID || first.TagID != nil || first.CategoryID != nil {
		t.Fatal(first, second)
	}
	backendCall(t, f.app, "GET", f.root+"/subscription", f.token, "", 404)
	backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, `{"account_id":"`+f.actor+`"}`, 400)
	backendCall(t, f.app, "GET", "/sphere/publishers/"+f.pub.ID+"/collections/series/subscription", f.otherToken, "", 404)
	backendCall(t, f.app, "POST", f.root+"/unsubscribe", f.otherToken, "", 204)
	backendCall(t, f.app, "POST", f.root+"/unsubscribe", f.otherToken, "", 204)
	third := backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 201))
	if third.ID == first.ID {
		t.Fatal("resubscribe reused ended incarnation")
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_taxonomy.post_category_subscriptions WHERE account_id=$1 AND collection_id=$2`, f.other, f.collection.ID); n != 2 {
		t.Fatal(n)
	}
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_post_interaction_audit WHERE collection_id=$1 AND action IN('collection.subscribe','collection.unsubscribe')`, f.collection.ID); n != 3 {
		t.Fatal(n)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.other, []authority.Rule{{Pattern: "posts.read"}}, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", f.root+"/subscription", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", f.root+"/unsubscribe", f.otherToken, "", 403)
	backendCall(t, f.app, "DELETE", f.root, f.token, "", 204)
	backendCall(t, f.app, "GET", f.root+"/subscription", f.otherToken, "", 404)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_taxonomy.post_category_subscriptions WHERE collection_id=$1 AND deleted_at IS NULL`, f.collection.ID); n != 0 {
		t.Fatal("deleted collection retained live subscription", n)
	}
}
func TestPGCollectionNavigationAuthoredOrderingAndHiddenAnchors(t *testing.T) {
	f := newCollectionNavigationFixture(t)
	visible := []posts.Post{collectionNavigationPost(t, f, posts.Public, "newer"), collectionNavigationPost(t, f, posts.Public, "tie-a"), collectionNavigationPost(t, f, posts.Public, "tie-b"), collectionNavigationPost(t, f, posts.Public, "older")}
	hidden := collectionNavigationPost(t, f, posts.Private, "private-between")
	unlisted := collectionNavigationPost(t, f, posts.Unlisted, "unlisted-between")
	draft := backendDecode[posts.Post](t, backendCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, `{"content":"draft-anchor","drafted_at":"2026-01-01T00:00:00Z"}`, 200))
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for i, p := range visible {
		offset := i
		if i == 2 {
			offset = 1
		}
		query(t, f.db, `UPDATE owned_text_posts SET published_at=$2,created_at=$2 WHERE id=$1`, p.ID, at.Add(-time.Duration(offset)*time.Minute))
	}
	query(t, f.db, `UPDATE owned_text_posts SET published_at=$2 WHERE id IN($1,$3)`, hidden.ID, at.Add(-30*time.Second), unlisted.ID)
	ids := []string{hidden.ID, unlisted.ID, draft.ID}
	for _, p := range visible {
		ids = append(ids, p.ID)
	}
	collectionNavigationAdd(t, f, ids...)
	sort.Slice(visible[1:3], func(i, j int) bool { return visible[1+i].ID > visible[1+j].ID })
	for i, p := range visible {
		for _, next := range []bool{false, true} {
			direction := "prev"
			index := i - 1
			if next {
				direction = "next"
				index = i + 1
			}
			want := 200
			if index < 0 || index >= len(visible) {
				want = 404
			}
			w := backendCall(t, f.app, "GET", f.root+"/posts/"+p.ID+"/"+direction, "", "", want)
			if want == 200 && backendDecode[posts.Post](t, w).ID != visible[index].ID {
				t.Fatal("wrong authored neighbor", i, direction, w.Body.String())
			}
		}
	}
	for _, id := range []string{hidden.ID, unlisted.ID, draft.ID, authorityUUID(t)} {
		for _, dir := range []string{"prev", "next"} {
			w := backendCall(t, f.app, "GET", f.root+"/posts/"+id+"/"+dir, "", "", 404)
			if !strings.Contains(w.Body.String(), "COLLECTION_POST_NOT_FOUND") {
				t.Fatal(w.Body.String())
			}
		}
	}
	w := backendCall(t, f.app, "GET", f.root+"/posts?offset=1&take=2", "", "", 200)
	page := backendDecode[[]posts.Post](t, w)
	if len(page) != 2 || page[0].ID != visible[1].ID || page[1].ID != visible[2].ID || w.Header().Get("X-Total") != "4" {
		t.Fatal(page, w.Header())
	}
	backendCall(t, f.app, "PUT", f.root+"/posts/reorder", f.token, `{"post_ids":[]}`, 400)
}
func TestPGCollectionNavigationNestedMediaAndCurrentPrivacy(t *testing.T) {
	f := newCollectionNavigationFixture(t)
	anchor := collectionNavigationPost(t, f, posts.Public, "anchor")
	parent := collectionNavigationPost(t, f, posts.Private, "hidden-parent-sentinel")
	nestedFile := collectionNavigationFile(t, f.db, f.actor, parent.ID)
	target := backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": "neighbor", "replied_post_id": parent.ID, "tags": []string{"navigation-tag"}}, 200))
	file := collectionNavigationFile(t, f.db, f.actor, target.ID)
	collectionNavigationAdd(t, f, anchor.ID, target.ID)
	query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, anchor.ID)
	query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, target.ID)
	path := f.root + "/posts/" + anchor.ID + "/next"
	w := backendCall(t, f.app, "GET", path, f.otherToken, "", 200)
	got := backendDecode[posts.Post](t, w)
	if got.ID != target.ID || got.RepliedPostID != nil || got.RepliedPost != nil || len(got.Attachments) != 1 || len(got.Tags) != 1 || len(got.PublisherCollections) != 1 || strings.Contains(w.Body.String(), nestedFile) || strings.Contains(w.Body.String(), parent.ID) || strings.Contains(w.Body.String(), "hidden-parent-sentinel") {
		t.Fatal(w.Body.String())
	}
	query(t, f.db, `UPDATE owned_media_files SET state='deleted',charge='none' WHERE id=$1`, file)
	got = backendDecode[posts.Post](t, backendCall(t, f.app, "GET", path, f.otherToken, "", 200))
	if len(got.Attachments) != 0 {
		t.Fatal("stale attachment", got.Attachments)
	}
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+target.ID, f.token, `{"visibility":3}`, 200)
	backendCall(t, f.app, "GET", path, f.otherToken, "", 404)
	backendCall(t, f.app, "PATCH", "/sphere/posts/"+target.ID, f.token, `{"visibility":0}`, 200)
	backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 201)
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/block", f.token, `{}`, 200)
	backendCall(t, f.app, "GET", path, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", f.root+"/subscription", f.otherToken, "", 404)
	backendCall(t, f.app, "GET", path, "", "", 200)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	backendCall(t, f.app, "GET", path, "", "", 404)
}
func TestPGCollectionSubscriptionConcurrentAndAuditRollback(t *testing.T) {
	f := newCollectionNavigationFixture(t)
	db2 := database(t)
	otherApp := backendApp(t, db2, f.config)
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for _, h := range []http.Handler{f.app, otherApp} {
		wg.Add(1)
		go func(h http.Handler) {
			defer wg.Done()
			<-start
			responses <- <-collectionAsync(context.Background(), h, "POST", f.root+"/subscribe", f.otherToken)
		}(h)
	}
	close(start)
	wg.Wait()
	close(responses)
	statuses := map[int]int{}
	id := ""
	for w := range responses {
		statuses[w.Code]++
		v := backendDecode[interactions.CollectionSubscription](t, w)
		if id != "" && id != v.ID {
			t.Fatal("duplicate incarnation", id, v)
		}
		id = v.ID
	}
	if statuses[201] != 1 || statuses[200] != 1 {
		t.Fatal(statuses)
	}
	// Reciprocal subscriptions lock the same two account owners in sorted order.
	otherOwner := f.backendFixture
	otherOwner.actor, otherOwner.token = f.other, f.otherToken
	otherPublisher := subscriptionPublisher(t, otherOwner)
	otherRoot := "/sphere/publishers/" + otherPublisher.Name + "/collections"
	backendCall(t, f.app, "POST", otherRoot, f.otherToken, `{"slug":"reciprocal"}`, 201)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	one := collectionAsync(ctx, f.app, "POST", otherRoot+"/reciprocal/subscribe", f.token)
	two := collectionAsync(ctx, otherApp, "POST", f.root+"/subscribe", f.otherToken)
	if w := <-one; w.Code != 201 {
		t.Fatal("reciprocal subscribe", w.Code, w.Body.String())
	}
	if w := <-two; w.Code != 200 {
		t.Fatal("reciprocal idempotence", w.Code, w.Body.String())
	}
	query(t, f.db, `ALTER TABLE owned_post_interaction_audit ADD CONSTRAINT collection_subscription_synthetic_failure CHECK(collection_id<>'`+f.collection.ID+`'::uuid OR action<>'collection.unsubscribe')`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_post_interaction_audit DROP CONSTRAINT IF EXISTS collection_subscription_synthetic_failure`)
	})
	backendCall(t, f.app, "POST", f.root+"/unsubscribe", f.otherToken, "", 503)
	got := backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "GET", f.root+"/subscription", f.otherToken, "", 200))
	if got.ID != id {
		t.Fatal("audit rollback lost subscription", got)
	}
}
func TestPGCollectionSubscriptionWaitRechecksIdentityAndProof(t *testing.T) {
	for _, change := range []string{"rename", "delete", "session", "scope", "expiry"} {
		t.Run(change, func(t *testing.T) {
			f := newCollectionNavigationFixture(t)
			var expires time.Time
			if change == "expiry" {
				f.config.Now = func() time.Time { return time.Now().UTC() }
				f.config.AccessTTL = 5 * time.Second
				issuer, e := authority.New(authoritypg.New(f.db), f.config)
				if e != nil {
					t.Fatal(e)
				}
				f.otherToken = pgIssue(t, issuer, f.other).Token
				raw, e := base64.RawURLEncoding.DecodeString(strings.Split(f.otherToken, ".")[1])
				if e != nil {
					t.Fatal(e)
				}
				var claims struct{ Exp int64 }
				if e = json.Unmarshal(raw, &claims); e != nil {
					t.Fatal(e)
				}
				expires = time.Unix(claims.Exp, 0)
				f.app = backendApp(t, f.db, f.config)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_publishers", f.pub.ID)
			done := collectionAsync(ctx, f.app, "POST", f.root+"/subscribe", f.otherToken)
			waitAtomicBlocked(t, locks, pid, "owned_publishers")
			var e error
			switch change {
			case "rename":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_publishers SET name=name||'-renamed' WHERE id=$1`, f.pub.ID)
			case "delete":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_post_collections SET deleted_at=clock_timestamp() WHERE id=$1`, f.collection.ID)
			case "session":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
			case "scope":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET scopes='["posts.read"]'::jsonb WHERE account_id=$1`, f.other)
			case "expiry":
				organizationWaitExpired(t, ctx, f.db, expires)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if change == "rename" {
				if got.Code != 201 {
					t.Fatal("canonical retained alias failed", got.Code, got.Body.String())
				}
				v := backendDecode[interactions.CollectionSubscription](t, got)
				if v.CollectionID == nil || *v.CollectionID != f.collection.ID {
					t.Fatal("alias changed collection identity", v)
				}
				return
			}
			if got.Code < 400 {
				t.Fatal("stale authority or resolver committed", change, got.Code, got.Body.String())
			}
			if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_taxonomy.post_category_subscriptions WHERE account_id=$1 AND collection_id=$2`, f.other, f.collection.ID); n != 0 {
				t.Fatal("waiter wrote subscription", n)
			}
		})
	}
}
func TestPGCollectionNavigationMediaWaitRechecksProjectionAndProof(t *testing.T) {
	for _, change := range []string{"media", "assignment", "nested", "relation", "session", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f := newCollectionNavigationFixture(t)
			anchor := collectionNavigationPost(t, f, posts.Public, "anchor")
			parent := collectionNavigationPost(t, f, posts.Public, "nested-public-to-private")
			target := backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": "target", "replied_post_id": parent.ID}, 200))
			file := collectionNavigationFile(t, f.db, f.actor, target.ID)
			collectionNavigationAdd(t, f, anchor.ID, target.ID)
			query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, anchor.ID)
			query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, target.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, lockedID := "owned_media_files", file
			if change == "nested" {
				table, lockedID = "owned_text_posts", parent.ID
			}
			blocker, pid := atomicBlocker(t, locks, table, lockedID)
			requestCtx, cancelRequest := context.WithCancel(ctx)
			defer cancelRequest()
			done := collectionAsync(requestCtx, f.app, "GET", f.root+"/posts/"+anchor.ID+"/next", f.otherToken)
			waitAtomicBlocked(t, locks, pid, table)
			var e error
			switch change {
			case "media":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_media_files SET state='deleted',charge='none' WHERE id=$1`, file)
			case "assignment":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_post_media SET owner_authorized=false WHERE post_id=$1 AND file_id=$2`, target.ID, file)
			case "relation":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_post_relations SET replied_post_id=NULL WHERE post_id=$1`, target.ID)
			case "nested":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_text_posts SET visibility=3 WHERE id=$1`, parent.ID)
			case "session":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
			case "cancel":
				cancelRequest()
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			w := <-done
			if change == "session" || change == "cancel" {
				if w.Code < 400 {
					t.Fatal("waiter retained proof", w.Code, w.Body.String())
				}
				if change == "cancel" {
					fresh := <-collectionAsync(ctx, f.app, "GET", f.root+"/posts/"+anchor.ID+"/next", f.otherToken)
					if fresh.Code != 200 {
						t.Fatal("cancelled navigation retained locks", fresh.Code, fresh.Body.String())
					}
				}
				return
			}
			if change == "relation" {
				if w.Code != 409 {
					t.Fatal("changed relation scope did not conflict", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			p := backendDecode[posts.Post](t, w)
			if (change == "media" || change == "assignment") && len(p.Attachments) != 0 || change == "nested" && (p.RepliedPostID != nil || p.RepliedPost != nil || strings.Contains(w.Body.String(), parent.ID)) {
				t.Fatal("stale nested projection", w.Body.String())
			}
		})
	}

	t.Run("cross_publisher_owner", func(t *testing.T) {
		f := newCollectionNavigationFixture(t)
		other := f.backendFixture
		other.actor, other.token = f.other, f.otherToken
		publisher := subscriptionPublisher(t, other)
		parent := backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+publisher.ID, f.otherToken, map[string]any{"content": "cross-publisher-private-source"}, 200))
		file := collectionNavigationFile(t, f.db, f.other, parent.ID)
		anchor := collectionNavigationPost(t, f, posts.Public, "anchor")
		target := backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": "target", "replied_post_id": parent.ID}, 200))
		collectionNavigationAdd(t, f, anchor.ID, target.ID)
		query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, anchor.ID)
		query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, target.ID)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.other)
		done := collectionAsync(ctx, f.app, "GET", f.root+"/posts/"+anchor.ID+"/next", f.token)
		waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
		if _, e := blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other); e != nil {
			t.Fatal(e)
		}
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		response := <-done
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		p := backendDecode[posts.Post](t, response)
		if p.RepliedPostID != nil || p.RepliedPost != nil || strings.Contains(response.Body.String(), parent.ID) || strings.Contains(response.Body.String(), file) {
			t.Fatal("disabled nested publisher owner leaked", response.Body.String())
		}
	})
}

func TestPGCollectionNavigationSinglePoolAndRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "collection-subscription-navigation.json"
	type saved struct {
		Actor, Other, Token, OtherToken, Root, Collection, Subscription, Anchor, Target, AuthorityState, Rows, Payload string
		Authority                                                                                                      restartAuthoritySnapshot
	}
	snapshot := func(db *sql.DB, c string) string {
		var raw string
		if e := db.QueryRow(`SELECT jsonb_build_object('subscriptions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM owned_taxonomy.post_category_subscriptions s WHERE collection_id=$1),'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM owned_post_interaction_audit a WHERE collection_id=$1),'items',(SELECT jsonb_agg(to_jsonb(i) ORDER BY post_id) FROM owned_post_collection_items i WHERE collection_id=$1))::text`, c).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		return raw
	}
	if phase == "before" {
		f := newCollectionNavigationFixture(t)
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
		a := collectionNavigationPost(t, f, posts.Public, "durable-anchor")
		parent := collectionNavigationPost(t, f, posts.Private, "durable-private-parent")
		b := backendDecode[posts.Post](t, backendJSONCall(t, f.app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": "durable-target", "replied_post_id": parent.ID}, 200))
		collectionNavigationFile(t, f.db, f.actor, b.ID)
		collectionNavigationAdd(t, f, a.ID, b.ID)
		query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, a.ID)
		query(t, f.db, `UPDATE owned_text_posts SET published_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, b.ID)
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		app := backendApp(t, db, f.config)
		sub := backendDecode[interactions.CollectionSubscription](t, backendCall(t, app, "POST", f.root+"/subscribe", f.otherToken, "", 201))
		payload := backendCall(t, app, "GET", f.root+"/posts/"+a.ID+"/next", f.otherToken, "", 200).Body.String()
		if strings.Contains(payload, parent.ID) {
			t.Fatal("private restart source leaked", payload)
		}
		saveRestartFixture(t, marker, saved{f.actor, f.other, f.token, f.otherToken, f.root, f.collection.ID, sub.ID, a.ID, b.ID, compositionAuthorityState(t, db, f.actor, f.other), snapshot(db, f.collection.ID), payload, saveAuthoritySnapshot(t, f.config)})
		return
	}
	var s saved
	loadRestartFixture(t, marker, &s)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	app := backendApp(t, db, restoreAuthoritySnapshot(t, s.Authority))
	if compositionAuthorityState(t, db, s.Actor, s.Other) != s.AuthorityState || snapshot(db, s.Collection) != s.Rows {
		t.Fatal("original state changed across restart")
	}
	sub := backendDecode[interactions.CollectionSubscription](t, backendCall(t, app, "GET", s.Root+"/subscription", s.OtherToken, "", 200))
	if sub.ID != s.Subscription {
		t.Fatal(sub)
	}
	response := backendCall(t, app, "GET", s.Root+"/posts/"+s.Anchor+"/next", s.OtherToken, "", 200)
	if response.Body.String() != s.Payload {
		t.Fatal("original nested/media projection changed across restart", response.Body.String())
	}
	p := backendDecode[posts.Post](t, response)
	if p.ID != s.Target || p.Content != "durable-target" || len(p.Attachments) != 1 || p.RepliedPostID != nil {
		t.Fatal(p)
	}
	backendCall(t, app, "POST", s.Root+"/subscribe", s.OtherToken, "", 200)
	if snapshot(db, s.Collection) != s.Rows || compositionAuthorityState(t, db, s.Actor, s.Other) != s.AuthorityState {
		t.Fatal("idempotent replay changed persisted state")
	}
}

func TestPGCollectionNavigationRealmPrivacyAndMembership(t *testing.T) {
	f := postRealmFixture(t)
	app := backendApp(t, f.db, f.config)
	c, e := f.interactions.CreateCollection(f.ctx, f.actor, f.individual.Name, interactions.CollectionRequest{Slug: "realm-series"})
	if e != nil {
		t.Fatal(e)
	}
	anchor, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("public-anchor")})
	if e != nil {
		t.Fatal(e)
	}
	middle := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.private.ID, "private-realm-middle")
	end, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("public-end")})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.interactions.AddCollectionPosts(f.ctx, f.actor, f.individual.Name, c.Slug, []string{anchor.ID, middle.ID, end.ID}, true); e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Add(-time.Hour)
	for i, id := range []string{anchor.ID, middle.ID, end.ID} {
		query(t, f.db, `UPDATE owned_text_posts SET published_at=$2 WHERE id=$1`, id, at.Add(-time.Duration(i)*time.Minute))
	}
	root := "/sphere/publishers/" + f.individual.Name + "/collections/" + c.Slug + "/posts/"
	for _, token := range []string{"", f.outsideToken} {
		p := backendDecode[posts.Post](t, backendCall(t, app, "GET", root+anchor.ID+"/next", token, "", 200))
		if p.ID != end.ID {
			t.Fatal("private realm candidate leaked", p)
		}
		backendCall(t, app, "GET", root+middle.ID+"/next", token, "", 404)
	}
	p := backendDecode[posts.Post](t, backendCall(t, app, "GET", root+anchor.ID+"/next", f.otherToken, "", 200))
	if p.ID != middle.ID {
		t.Fatal("current realm membership ignored", p)
	}
	if _, e = f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	p = backendDecode[posts.Post](t, backendCall(t, app, "GET", root+anchor.ID+"/next", f.otherToken, "", 200))
	if p.ID != end.ID {
		t.Fatal("revoked member retained private neighbor", p)
	}
}
func TestPGCollectionSubscriptionRenameAndAdminDeleteIsolation(t *testing.T) {
	f := newCollectionNavigationFixture(t)
	sub := backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "POST", f.root+"/subscribe", f.otherToken, "", 201))
	renamed := f.pub.Name + "-renamed"
	query(t, f.db, `UPDATE owned_publishers SET name=$2 WHERE id=$1`, f.pub.ID, renamed)
	newRoot := "/sphere/publishers/" + renamed + "/collections/series"
	got := backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "GET", newRoot+"/subscription", f.otherToken, "", 200))
	if got.ID != sub.ID {
		t.Fatal("identity rename lost subscription", got)
	}
	got = backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "GET", f.root+"/subscription", f.otherToken, "", 200))
	if got.ID != sub.ID {
		t.Fatal("retired alias changed subscription", got)
	}
	otherPub := authorityUUID(t)
	otherCollection := authorityUUID(t)
	_, e := f.db.Exec(`INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at)VALUES($1,$2,'Synthetic reused handle',0,$3,clock_timestamp(),clock_timestamp())`, otherPub, f.pub.Name, f.actor)
	var conflict *pgconn.PgError
	if !errors.As(e, &conflict) || conflict.Code != "23505" {
		t.Fatal("retired name reservation was transferable", e)
	}
	otherName := f.pub.ID // A UUID-shaped handle names this distinct publisher; it is never an ID fallback.
	query(t, f.db, `INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at)VALUES($1,$2,'Synthetic different identity',0,$3,clock_timestamp(),clock_timestamp())`, otherPub, otherName, f.actor)
	query(t, f.db, `INSERT INTO owned_post_collections(id,publisher_id,slug,created_at,updated_at)VALUES($1,$2,'series',clock_timestamp(),clock_timestamp())`, otherCollection, otherPub)
	otherRoot := "/sphere/publishers/" + otherName + "/collections/series"
	backendCall(t, f.app, "GET", otherRoot+"/subscription", f.otherToken, "", 404)
	newSub := backendDecode[interactions.CollectionSubscription](t, backendCall(t, f.app, "POST", otherRoot+"/subscribe", f.otherToken, "", 201))
	if newSub.ID == sub.ID || newSub.CollectionID == nil || *newSub.CollectionID != otherCollection {
		t.Fatal("handle reuse inherited old subscription", newSub)
	}
	// Explicit global authority is still required by the existing admin route.
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.actor)
	backendCall(t, f.app, "DELETE", "/sphere/admin/collections/"+f.collection.ID, f.token, "", 204)
	backendCall(t, f.app, "GET", newRoot+"/subscription", f.otherToken, "", 404)
	if n := subscriptionCount(t, f.db, `SELECT count(*) FROM owned_taxonomy.post_category_subscriptions WHERE collection_id=$1 AND deleted_at IS NULL`, f.collection.ID); n != 0 {
		t.Fatal("admin deletion retained live incarnation", n)
	}
	backendCall(t, f.app, "GET", f.root+"/subscription", f.otherToken, "", 404)
	backendCall(t, f.app, "GET", otherRoot+"/subscription", f.otherToken, "", 200)
}
