// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
	"sync"
	"testing"
	"time"
)

func timelineDB(t *testing.T) *sql.DB {
	t.Helper()
	db := interactionDB(t)
	var ready bool
	if e := db.QueryRow(`SELECT to_regclass('owned_post_relations') IS NOT NULL AND to_regclass('owned_post_watch_preferences') IS NOT NULL`).Scan(&ready); e != nil || !ready {
		t.Fatal("timeline schema must be explicitly installed by guarded disposable harness", e)
	}
	return db
}
func timelineRepo(db *sql.DB) (*timelinepg.Repository, *postpg.Repository) {
	visibility := socialpg.New(db)
	associations := interactionpg.New(db).WithVisibility(visibility)
	r := timelinepg.New(db).WithVisibility(visibility).WithAssociations(associations)
	return r, postpg.New(db).WithVisibility(visibility).WithAssociations(r)
}
func relatedPost(t *testing.T, p *postpg.Repository, actor, pub, body string, visibility int, reply, forward, chain *string) posts.Post {
	t.Helper()
	v, e := p.Create(context.Background(), actor, pub, posts.Patch{Content: &body, Visibility: &visibility, RepliedPostID: reply, ForwardedPostID: forward, ChainedPostID: chain})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGTimelineFeedSearchPrivacyAndCursor(t *testing.T) {
	db := timelineDB(t)
	r, p := timelineRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3000)
	viewer := postAccount(t, db, 3001)
	public := []posts.Post{}
	for _, body := range []string{"timeline needle alpha", "timeline needle beta", "literal 100%_ end"} {
		public = append(public, createPost(t, p, owner, pub.Name, body, posts.Public, false))
	}
	query(t, db, `UPDATE owned_text_posts SET published_at='2026-01-01T00:00:00Z' WHERE publisher_id=$1`, pub.ID)
	private := createPost(t, p, owner, pub.Name, "private needle", posts.Private, false)
	draft := createPost(t, p, owner, pub.Name, "draft needle", posts.Public, true)
	unlisted := createPost(t, p, owner, pub.Name, "unlisted needle", posts.Unlisted, false)
	friends := createPost(t, p, owner, pub.Name, "friends needle", posts.Friends, false)
	close := createPost(t, p, owner, pub.Name, "close needle", posts.CloseFriends, false)
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 4; i++ {
		page, e := r.Feed(ctx, "", timeline.Options{Take: 1, Publisher: pub.Name, Mode: sphere.Latest, Cursor: cursor})
		if e != nil {
			t.Fatal(e)
		}
		for _, event := range page.Items {
			if event.Type != "posts.new" || event.Data == nil || event.ID != event.Data.ID || event.ResourceIdentifier != event.ID {
				t.Fatal("client event contract", event)
			}
			if seen[event.ID] {
				t.Fatal("duplicate cursor row", event.ID)
			}
			seen[event.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatal("equal timestamp cursor skipped posts", seen)
	}
	for _, hidden := range []posts.Post{private, draft, unlisted, friends, close} {
		if seen[hidden.ID] {
			t.Fatal("feed visibility leak", hidden.ID)
		}
	}
	list, total, e := r.Search(ctx, viewer, timeline.SearchOptions{Publisher: pub.Name, Query: "needle", Take: 1, Offset: 1})
	if e != nil || total != 2 || len(list) != 1 {
		t.Fatal(list, total, e)
	}
	list, total, e = r.Search(ctx, "", timeline.SearchOptions{Publisher: pub.Name, Query: "%_", Take: 20})
	if e != nil || total != 1 || list[0].ID != public[2].ID {
		t.Fatal("search wildcards treated as patterns", list, total, e)
	}
	socialRepo := socialpg.New(db)
	if _, e = socialRepo.ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.RequestFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e = socialRepo.ChangeRelationship(ctx, viewer, owner, social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
		t.Fatal(e)
	}
	list, total, e = r.Search(ctx, viewer, timeline.SearchOptions{Publisher: pub.Name, Query: "needle", Take: 20})
	if e != nil || total != 3 {
		t.Fatal("friends visibility", list, total, e)
	}
	if _, e = socialRepo.ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.SetClose}); e != nil {
		t.Fatal(e)
	}
	list, total, e = r.Search(ctx, viewer, timeline.SearchOptions{Publisher: pub.Name, Query: "needle", Take: 20})
	if e != nil || total != 4 {
		t.Fatal("close friends direction", list, total, e)
	}
	if _, e = socialRepo.ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = r.Search(ctx, viewer, timeline.SearchOptions{Publisher: pub.Name, Take: 20}); !errors.Is(e, timeline.ErrPublisherNotFound) {
		t.Fatal("blocked publisher scope", e)
	}
	if page, e := r.Feed(ctx, viewer, timeline.Options{Take: 100}); e != nil {
		t.Fatal(e)
	} else {
		for _, event := range page.Items {
			if event.Data != nil && event.Data.PublisherID == pub.ID {
				t.Fatal("blocked global feed leak")
			}
		}
	}
	query(t, db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, owner)
	if _, _, e = r.Search(ctx, "", timeline.SearchOptions{Publisher: pub.Name, Take: 20}); !errors.Is(e, timeline.ErrPublisherNotFound) {
		t.Fatal("disabled publisher leak", e)
	}
}
func TestPGTimelineReplyForwardThreadChainLifecycle(t *testing.T) {
	db := timelineDB(t)
	r, p := timelineRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3010)
	other, otherPub := postPublisher(t, db, 3011)
	root := createPost(t, p, owner, pub.Name, "root", posts.Public, false)
	reply := relatedPost(t, p, other, otherPub.Name, "reply", posts.Public, &root.ID, nil, nil)
	child := relatedPost(t, p, owner, pub.Name, "child", posts.Public, &reply.ID, nil, nil)
	private := relatedPost(t, p, owner, pub.Name, "private branch", posts.Private, &root.ID, nil, nil)
	_ = relatedPost(t, p, owner, pub.Name, "public under private", posts.Public, &private.ID, nil, nil)
	forward := relatedPost(t, p, other, otherPub.Name, "forward", posts.Public, nil, &root.ID, nil)
	chain := relatedPost(t, p, owner, pub.Name, "chain", posts.Public, nil, nil, &root.ID)
	chain2 := relatedPost(t, p, owner, pub.Name, "chain2", posts.Public, nil, nil, &chain.ID)
	got, e := p.Get(ctx, "", root.ID)
	if e != nil || got.RepliesCount != 1 || got.ForwardsCount != 1 || got.ThreadRepliesCount != 2 || got.ChainedCount != 2 {
		t.Fatal("visible relation counts", got, e)
	}
	thread, e := r.Thread(ctx, "", root.ID, timeline.ThreadOptions{Take: 1, Ancestors: true, AncestorLimit: 50})
	if e != nil || len(thread.Descendants) != 1 || !thread.HasMore || thread.Descendants[0].Post.ID != reply.ID {
		t.Fatal(thread, e)
	}
	thread, e = r.Thread(ctx, "", child.ID, timeline.ThreadOptions{Take: 20, Ancestors: true, AncestorLimit: 50})
	if e != nil || len(thread.Ancestors) != 2 || thread.Ancestors[0].Post.ID != root.ID || thread.Current.Depth != 2 {
		t.Fatal(thread, e)
	}
	flat, total, e := r.ThreadedReplies(ctx, "", root.ID, timeline.SearchOptions{Take: 20})
	if e != nil || total != 1 || len(flat) != 2 || flat[1].Depth != 1 {
		t.Fatal(flat, total, e)
	}
	members, e := r.Chain(ctx, "", chain2.ID)
	if e != nil || len(members) != 3 || members[0].ID != root.ID {
		t.Fatal(members, e)
	}
	if _, e = p.Create(ctx, other, otherPub.Name, posts.Patch{Content: postString("wrong chain"), ChainedPostID: &root.ID}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("cross publisher chain", e)
	}
	if _, e = p.Update(ctx, other, reply.ID, posts.Patch{RepliedPostID: &forward.ID}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("relation reassignment", e)
	}
	if _, e = p.Create(ctx, other, otherPub.Name, posts.Patch{Content: postString("private target"), RepliedPostID: &private.ID}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private reply target", e)
	}
	if _, e = p.Update(ctx, owner, root.ID, posts.Patch{Visibility: postInt(posts.Private)}); e != nil {
		t.Fatal(e)
	}
	got, e = p.Get(ctx, "", forward.ID)
	if e != nil || got.ForwardedPostID != nil || got.ForwardedPost != nil {
		t.Fatal("nested private target or ID leaked", got, e)
	}
	if _, _, e = r.Related(ctx, "", root.ID, "replies", timeline.SearchOptions{Take: 20}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("hidden anchor count oracle", e)
	}
	if e = p.Delete(ctx, owner, root.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Chain(ctx, "", chain.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted chain head", e)
	}
	got, e = p.Get(ctx, "", reply.ID)
	if e != nil || got.RepliedPostID != nil || got.RepliedPost != nil {
		t.Fatal("deleted nested reply", got, e)
	}
	if e = accountpg.NewForCompatibilityTests(db).DeletePublisher(ctx, other, otherPub.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Thread(ctx, "", reply.ID, timeline.ThreadOptions{Take: 20}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted publisher thread", e)
	}
}
func TestPGTimelineFollowsPendingIsolationAndConcurrency(t *testing.T) {
	db := timelineDB(t)
	r, p := timelineRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3020)
	viewer := postAccount(t, db, 3021)
	another, otherPub := postPublisher(t, db, 3022)
	post := createPost(t, p, owner, pub.Name, "followed public", posts.Public, false)
	_ = createPost(t, p, owner, pub.Name, "following is not private access", posts.Private, false)
	query(t, db, `INSERT INTO owned_publisher_follow_policy(publisher_id,requires_approval)VALUES($1,true)`, pub.ID)
	sub, e := r.Subscribe(ctx, viewer, pub.Name)
	if e != nil || !sub.IsPending || sub.IsActive || sub.FollowRequest == nil {
		t.Fatal(sub, e)
	}
	page, e := r.Feed(ctx, viewer, timeline.Options{Take: 20, Filter: "subscriptions"})
	if e != nil || page.Items[0].Type != "empty" {
		t.Fatal("pending request granted feed subscription", page, e)
	}
	if _, e = r.DecideFollow(ctx, another, otherPub.Name, sub.FollowRequest.ID, true, nil); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("cross publisher request mutation", e)
	}
	if _, e = r.DecideFollow(ctx, viewer, pub.Name, sub.FollowRequest.ID, true, nil); e == nil {
		t.Fatal("non-manager approval")
	}
	accepted, e := r.DecideFollow(ctx, owner, pub.Name, sub.FollowRequest.ID, true, nil)
	if e != nil || !accepted.IsActive {
		t.Fatal(accepted, e)
	}
	page, e = r.Feed(ctx, viewer, timeline.Options{Take: 20, Filter: "subscriptions"})
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != post.ID {
		t.Fatal("follow visibility", page, e)
	}
	if _, e = r.Unsubscribe(ctx, viewer, pub.Name); e != nil {
		t.Fatal(e)
	}
	query(t, db, `UPDATE owned_publisher_follow_policy SET requires_approval=false WHERE publisher_id=$1`, pub.ID)
	rs := []*timelinepg.Repository{r, timelinepg.New(database(t)).WithVisibility(socialpg.New(db))}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, repo := range rs {
		wg.Add(1)
		go func(repo *timelinepg.Repository) {
			defer wg.Done()
			<-start
			_, e := repo.Subscribe(ctx, viewer, pub.Name)
			errs <- e
		}(repo)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_publisher_follows WHERE account_id=$1 AND publisher_id=$2 AND state='accepted' AND ended_at IS NULL`, viewer, pub.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate subscription", count, e)
	}
	if _, e = socialpg.New(db).ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Subscribe(ctx, viewer, pub.Name); !errors.Is(e, timeline.ErrPublisherNotFound) {
		t.Fatal("blocked follow", e)
	}
	if list, total, e := r.Subscriptions(ctx, viewer, 0, 20); e != nil || total != 0 || len(list) != 0 {
		t.Fatal("blocked subscription list", list, total, e)
	}
	if _, e = r.Unsubscribe(ctx, viewer, pub.Name); e != nil {
		t.Fatal("cleanup blocked relationship", e)
	}
}
func TestPGTimelineDiscoveryFeedbackAndAuditRollback(t *testing.T) {
	db := timelineDB(t)
	r, p := timelineRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3030)
	viewer := postAccount(t, db, 3031)
	post, e := p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("discoverable article"), Type: postInt(posts.Article), Tags: strList("timeline-interest")})
	if e != nil {
		t.Fatal(e)
	}
	private := createPost(t, p, owner, pub.Name, "private feedback", posts.Private, false)
	if _, e = r.Feedback(ctx, viewer, timeline.FeedbackRequest{Kind: "post", ReferenceID: private.ID, Feedback: "positive"}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private feedback target", e)
	}
	feedback, e := r.Feedback(ctx, viewer, timeline.FeedbackRequest{Kind: "post", ReferenceID: post.ID, Feedback: "positive"})
	if e != nil || len(feedback.UpdatedProfiles) != 2 {
		t.Fatal(feedback, e)
	}
	profile, e := r.Discovery(ctx, viewer)
	if e != nil || len(profile.Interests) != 2 || len(profile.SuggestedPublishers) == 0 || profile.SuggestedPublishers[0].ReferenceID != pub.ID {
		t.Fatal(profile, e)
	}
	if other, e := r.Discovery(ctx, owner); e != nil || len(other.Interests) != 0 {
		t.Fatal("account interest leakage", other, e)
	}
	feedback, e = r.Feedback(ctx, viewer, timeline.FeedbackRequest{Kind: "publisher", ReferenceID: pub.ID, Feedback: "negative", Suppress: true})
	if e != nil || feedback.Preference == nil {
		t.Fatal(feedback, e)
	}
	page, e := r.Feed(ctx, viewer, timeline.Options{Publisher: pub.Name, Take: 20})
	if e != nil || page.Items[0].Type != "empty" {
		t.Fatal("suppression ignored", page, e)
	}
	if e = r.Unsuppress(ctx, owner, "publisher", pub.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("cross actor suppression delete", e)
	}
	if e = r.Unsuppress(ctx, viewer, "publisher", pub.ID); e != nil {
		t.Fatal(e)
	}
	query(t, db, `ALTER TABLE owned_timeline_audit ADD CONSTRAINT timeline_synthetic_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000003031'::uuid)`)
	t.Cleanup(func() {
		query(t, db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT IF EXISTS timeline_synthetic_failure`)
	})
	if _, e = r.AdjustWeight(ctx, viewer, timeline.WeightRequest{Kind: "publisher", ReferenceID: pub.ID, ScoreDelta: 10}); e == nil {
		t.Fatal("audit failure ignored")
	}
	if _, e = r.Subscribe(ctx, viewer, pub.Name); e == nil {
		t.Fatal("subscription audit failure ignored")
	}
	var score float64
	if e = db.QueryRow(`SELECT score FROM owned_timeline_interests WHERE account_id=$1 AND kind='publisher' AND reference_id=$2`, viewer, pub.ID).Scan(&score); e != nil || score != -26 {
		t.Fatal("partial feedback update", score, e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_publisher_follows WHERE account_id=$1`, viewer).Scan(&count); e != nil || count != 0 {
		t.Fatal("partial follow write", count, e)
	}
	query(t, db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT timeline_synthetic_failure`)
	if _, e = r.Subscribe(ctx, viewer, pub.Name); e != nil {
		t.Fatal(e)
	}
	if n, e := r.ResetInterests(ctx, viewer); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestPGTimelineWatchCanonicalPrivacyAndAtomicPatch(t *testing.T) {
	db := timelineDB(t)
	r, p := timelineRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3040)
	viewer, viewerPub := postPublisher(t, db, 3041)
	post := createPost(t, p, owner, pub.Name, "watchable post", posts.Public, false)
	interactionsRepo := interactionpg.New(db).WithVisibility(socialpg.New(db))
	if _, e := interactionsRepo.Bookmark(ctx, viewer, post.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e := interactionsRepo.React(ctx, viewer, post.ID, interactions.ReactionRequest{Symbol: "heart"}); e != nil {
		t.Fatal(e)
	}
	_ = relatedPost(t, p, viewer, viewerPub.Name, "watch reply", posts.Public, &post.ID, nil, nil)
	watch := &sphere.WatchService{Repository: r}
	ids, e := watch.ResolveWatcherAccountIDs(ctx, r, []string{post.ID}, sphere.WatchReplies)
	if e != nil || len(ids) != 1 || ids[0] != viewer {
		t.Fatal(ids, e)
	}
	a, b := r, timelinepg.New(database(t))
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i, repo := range []*timelinepg.Repository{a, b} {
		go func(i int, repo *timelinepg.Repository) {
			<-start
			update := sphere.WatchPreferenceUpdate{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}
			if i == 1 {
				update = sphere.WatchPreferenceUpdate{Source: sphere.WatchBookmark, NotifyEdits: boolean(false)}
			}
			_, e := repo.PatchPreferences(ctx, viewer, []sphere.WatchPreferenceUpdate{update})
			errs <- e
		}(i, repo)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	prefs, e := r.WatchPreferences(ctx, viewer)
	if e != nil || prefs[0].NotifyReplies || prefs[0].NotifyEdits {
		t.Fatal("lost watch patch", prefs, e)
	}
	if e = r.SavePreferences(ctx, viewer, []sphere.WatchPreference{sphere.DefaultWatchPreference(owner, sphere.WatchBookmark)}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("cross actor watch write", e)
	}
	if _, e = p.Update(ctx, owner, post.ID, posts.Patch{Visibility: postInt(posts.Private)}); e != nil {
		t.Fatal(e)
	}
	ids, e = watch.ResolveWatcherAccountIDs(ctx, r, []string{post.ID}, sphere.WatchChains)
	if e != nil || len(ids) != 0 {
		t.Fatal("private watcher notification leak", ids, e)
	}
	if _, e = p.Update(ctx, owner, post.ID, posts.Patch{Visibility: postInt(posts.Public)}); e != nil {
		t.Fatal(e)
	}
	if _, e = socialpg.New(db).ChangeRelationship(ctx, viewer, owner, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	ids, e = watch.ResolveWatcherAccountIDs(ctx, r, []string{post.ID}, sphere.WatchChains)
	if e != nil || len(ids) != 0 {
		t.Fatal("blocked watcher notification leak", ids, e)
	}
}
func TestPGTimelineGuardedHTTPAndFreshAuthority(t *testing.T) {
	db := timelineDB(t)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3050)
	_, v, guard, token := interactionAuthority(t, db, owner)
	r := timelinepg.NewWithAuthority(db, guard).WithVisibility(socialpg.New(db)).WithAssociations(interactionpg.New(db))
	h := timeline.NewVerifiedHandler(r, v)
	if w := postHTTP(h, "POST", "/api/publishers/"+pub.Name+"/subscribe", "", token); w.Code != 403 {
		t.Fatal("missing grant", w.Code, w.Body.String())
	}
	if e := authoritypg.New(db).ReplacePolicy(ctx, owner, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	if w := postHTTP(h, "PUT", "/api/posts/watch", `{"bookmark":{"replies":false}}`, token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postHTTP(h, "POST", "/api/publishers/"+pub.Name+"/subscribe", "{}", token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postHTTP(h, "GET", "/api/timeline?mode=wrong", "", token); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/timeline", nil)
	req.Header.Set("X-Account-ID", owner)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("spoofed identity", w.Code)
	}
	proof, e := v.Authenticate(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var blocker int
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&blocker); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, owner); e != nil {
		t.Fatal(e)
	}
	waiting, cancel := context.WithTimeout(guard.Bind(ctx, proof), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := r.PatchWatch(waiting, owner, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(true)}})
		done <- e
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if e = db.QueryRowContext(waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case <-waiting.Done():
			t.Fatal("guard never waited")
		case <-ticker.C:
		}
	}
	if _, e = tx.Exec(`UPDATE owned_authority_sessions SET revoked_at=now() WHERE account_id=$1`, owner); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("stale proof wrote watch preferences")
	}
	prefs, e := timelinepg.New(db).WatchPreferences(ctx, owner)
	if e != nil || prefs[0].NotifyReplies {
		t.Fatal("revoked write persisted", prefs, e)
	}
	if w = postHTTP(h, "GET", "/api/posts/watch", "", token); w.Code != 401 {
		t.Fatal("revoked read", w.Code)
	}
}
func TestPGTimelineRestartPersistence(t *testing.T) {
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		r, p := timelineRepo(db)
		ctx := context.Background()
		root, e := p.GetSlug(ctx, "", "post-publisher-3090", "timeline-restart")
		if e != nil || root.RepliesCount != 1 {
			t.Fatal("restart relations missing", root, e)
		}
		status, e := r.SubscriptionStatus(ctx, "d5100000-0000-4000-8000-000000003091", "post-publisher-3090")
		if e != nil || !status.IsActive {
			t.Fatal("restart follows missing", status, e)
		}
		profile, e := r.Discovery(ctx, "d5100000-0000-4000-8000-000000003091")
		if e != nil || len(profile.Interests) != 1 {
			t.Fatal("restart interest missing", profile, e)
		}
		prefs, e := r.WatchPreferences(ctx, "d5100000-0000-4000-8000-000000003091")
		if e != nil || prefs[0].NotifyReplies {
			t.Fatal("restart watch missing", prefs, e)
		}
		return
	}
	db := timelineDB(t)
	r, p := timelineRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3090)
	viewer, viewerPub := postPublisher(t, db, 3091)
	root, e := p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("timeline restart root"), Slug: postString("timeline-restart")})
	if e != nil {
		t.Fatal(e)
	}
	_ = relatedPost(t, p, viewer, viewerPub.Name, "restart reply", posts.Public, &root.ID, nil, nil)
	if _, e = r.Subscribe(ctx, viewer, pub.Name); e != nil {
		t.Fatal(e)
	}
	if _, e = r.AdjustWeight(ctx, viewer, timeline.WeightRequest{Kind: "publisher", ReferenceID: pub.ID, ScoreDelta: 4}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.PatchPreferences(ctx, viewer, []sphere.WatchPreferenceUpdate{{Source: sphere.WatchBookmark, NotifyReplies: boolean(false)}}); e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(root)
	if e != nil || strings.Contains(string(b), "NaN") {
		t.Fatal(e)
	}
}

func TestPGTimelineRelationRollbackDepthAndCancellation(t *testing.T) {
	db := timelineDB(t)
	r, p := timelineRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 3060)
	root := createPost(t, p, owner, pub.Name, "rollback root", posts.Public, false)
	if _, e := postpg.New(db).Create(ctx, owner, pub.Name, posts.Patch{Content: postString("adapter absent"), RepliedPostID: &root.ID}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("relation silently ignored without adapter", e)
	}
	if _, e := postpg.NewWithAssociations(db, interactionpg.New(db)).Create(ctx, owner, pub.Name, posts.Patch{Content: postString("relation adapter absent"), RepliedPostID: &root.ID}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("relation silently ignored by interaction-only adapter", e)
	}
	query(t, db, `ALTER TABLE owned_timeline_audit ADD CONSTRAINT timeline_relation_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000003060'::uuid)`)
	t.Cleanup(func() {
		query(t, db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT IF EXISTS timeline_relation_failure`)
	})
	if _, e := p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("must roll back"), RepliedPostID: &root.ID, Tags: strList("timeline-rollback-orphan")}); e == nil {
		t.Fatal("relation audit failure ignored")
	}
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, pub.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("partial canonical post", count, e)
	}
	if e := db.QueryRow(`SELECT count(*) FROM owned_post_relations l JOIN owned_text_posts p ON p.id=l.post_id WHERE p.publisher_id=$1`, pub.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("orphan relation", count, e)
	}
	query(t, db, `ALTER TABLE owned_timeline_audit DROP CONSTRAINT timeline_relation_failure`)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := p.Create(cancelled, owner, pub.Name, posts.Patch{Content: postString("cancelled"), RepliedPostID: &root.ID}); e == nil {
		t.Fatal("cancelled relation write")
	}
	// Guarded synthetic seed builds an exact 50-node ancestry boundary without
	// relying on timing or a live server. Canonical creates must reject node 51.
	parent := root.ID
	for i := 0; i < 49; i++ {
		id := authorityUUID(t)
		query(t, db, `INSERT INTO owned_text_posts(id,publisher_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,'synthetic ancestry',0,0,now(),now(),now())`, id, pub.ID)
		query(t, db, `INSERT INTO owned_post_relations(post_id,replied_post_id)VALUES($1,$2)`, id, parent)
		parent = id
	}
	if _, e := p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("too deep"), RepliedPostID: &parent}); !errors.Is(e, timeline.ErrDepth) {
		t.Fatal("thread depth unbounded", e)
	}
	thread, e := r.Thread(ctx, "", parent, timeline.ThreadOptions{Take: 20, Ancestors: true, AncestorLimit: 50})
	if e != nil || len(thread.Ancestors) != 49 {
		t.Fatal("valid depth boundary", len(thread.Ancestors), e)
	}
	query(t, db, `INSERT INTO owned_post_relations(post_id,replied_post_id)VALUES($1,$2)`, root.ID, parent)
	if _, e = p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("cyclic ancestry"), RepliedPostID: &root.ID}); !errors.Is(e, timeline.ErrDepth) {
		t.Fatal("cycle not rejected", e)
	}
	if _, e = r.Thread(ctx, "", root.ID, timeline.ThreadOptions{Take: 20, Ancestors: true, AncestorLimit: 50}); !errors.Is(e, timeline.ErrDepth) {
		t.Fatal("cyclic read not bounded", e)
	}
}
