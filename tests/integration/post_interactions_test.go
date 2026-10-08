// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

func interactionDB(t *testing.T) *sql.DB {
	t.Helper()
	db := database(t)
	var ready bool
	if e := db.QueryRow(`SELECT to_regclass('owned_post_interaction_audit') IS NOT NULL AND (SELECT relkind='v' FROM pg_class WHERE oid='owned_taxonomy.posts'::regclass)`).Scan(&ready); e != nil || !ready {
		t.Fatal("canonical interaction schema must be installed by disposable harness", e)
	}
	return db
}
func interactionRepo(db *sql.DB) (*interactionpg.Repository, *postpg.Repository) {
	r := interactionpg.New(db)
	return r, postpg.NewWithAssociations(db, r)
}
func strList(v ...string) *[]string { return &v }
func interactionCollection(t *testing.T, r *interactionpg.Repository, actor, publisher, slug string) posts.Collection {
	t.Helper()
	c, e := r.CreateCollection(context.Background(), actor, publisher, interactions.CollectionRequest{Slug: slug})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestPGPostInteractionsReactionBookmarkPrivacy(t *testing.T) {
	db := interactionDB(t)
	r, p := interactionRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2000)
	other := postAccount(t, db, 2001)
	public := createPost(t, p, owner, pub.Name, "public interaction", posts.Public, false)
	private := createPost(t, p, owner, pub.Name, "private interaction", posts.Private, false)
	draft := createPost(t, p, owner, pub.Name, "draft interaction", posts.Public, true)
	for _, id := range []string{private.ID, draft.ID} {
		if _, _, e := r.React(ctx, other, id, interactions.ReactionRequest{Symbol: "heart"}); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("private react", e)
		}
		if _, e := r.Bookmark(ctx, other, id); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("private bookmark", e)
		}
		if _, _, e := r.Reactions(ctx, "", id, interactions.Page{Take: 20}); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("private reaction count", e)
		}
	}
	react, removed, e := r.React(ctx, other, public.ID, interactions.ReactionRequest{Symbol: "heart", Attitude: interactions.Positive})
	if e != nil || removed || react.AccountID != other {
		t.Fatal(react, removed, e)
	}
	got, e := p.Get(ctx, other, public.ID)
	if e != nil || got.ReactionsCount["heart"] != 1 || !got.ReactionsMade["heart"] {
		t.Fatal(got, e)
	}
	got, e = p.Get(ctx, "", public.ID)
	if e != nil || got.ReactionsCount["heart"] != 1 || len(got.ReactionsMade) != 0 {
		t.Fatal("actor state leak", got, e)
	}
	list, total, e := r.Reactions(ctx, "", public.ID, interactions.Page{Take: 20})
	if e != nil || total != 1 || len(list) != 1 || list[0].Account == nil || list[0].Account.ID != other {
		t.Fatal(list, total, e)
	}
	if list, total, e = r.Reactions(ctx, "", public.ID, interactions.Page{Take: 1, Offset: 1}); e != nil || total != 1 || len(list) != 0 {
		t.Fatal("Take then Skip", list, total, e)
	}
	b, e := r.Bookmark(ctx, other, public.ID)
	if e != nil {
		t.Fatal(e)
	}
	same, e := r.Bookmark(ctx, other, public.ID)
	if e != nil || same.ID != b.ID {
		t.Fatal("idempotent bookmark", same, e)
	}
	if got, e := r.BookmarkStatus(ctx, owner, public.ID); e != nil || got != nil {
		t.Fatal("bookmark actor leak", got, e)
	}
	if saved, n, e := r.Bookmarks(ctx, other, interactions.Page{Take: 20}); e != nil || n != 1 || len(saved) != 1 || saved[0].ID != public.ID {
		t.Fatal(saved, n, e)
	}
	if _, e = p.Update(ctx, owner, public.ID, posts.Patch{Visibility: postInt(posts.Private)}); e != nil {
		t.Fatal(e)
	}
	if saved, n, e := r.Bookmarks(ctx, other, interactions.Page{Take: 20}); e != nil || n != 0 || len(saved) != 0 {
		t.Fatal("changed visibility leaked bookmark", saved, n, e)
	}
	if e = r.Unbookmark(ctx, other, public.ID); e != nil {
		t.Fatal("cannot remove hidden bookmark", e)
	}
	if e = r.Unbookmark(ctx, other, public.ID); !errors.Is(e, interactions.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = p.Update(ctx, owner, public.ID, posts.Patch{Visibility: postInt(posts.Public)}); e != nil {
		t.Fatal(e)
	}
	if _, removed, e = r.React(ctx, other, public.ID, interactions.ReactionRequest{Symbol: "heart", Attitude: interactions.Negative}); e != nil || !removed {
		t.Fatal("toggle must match symbol, not supplied attitude", removed, e)
	}
	got, e = p.Get(ctx, other, public.ID)
	if e != nil || len(got.ReactionsCount) != 0 {
		t.Fatal("stale count", got, e)
	}
}
func TestPGPostInteractionsCollectionOwnershipAndBatch(t *testing.T) {
	db := interactionDB(t)
	r, p := interactionRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2010)
	other, foreign := postPublisher(t, db, 2011)
	editor := postAccount(t, db, 2012)
	joinPostEditor(t, db, owner, editor, pub.ID)
	c := interactionCollection(t, r, owner, pub.Name, " MiXeD ")
	if c.Slug != "mixed" || c.PublisherID != pub.ID {
		t.Fatal(c)
	}
	if _, e := r.CreateCollection(ctx, editor, pub.Name, interactions.CollectionRequest{Slug: "MIXED"}); !errors.Is(e, interactions.ErrCollectionConflict) {
		t.Fatal(e)
	}
	a := createPost(t, p, owner, pub.Name, "collection public", posts.Public, false)
	hidden := createPost(t, p, owner, pub.Name, "collection private", posts.Private, false)
	alien := createPost(t, p, other, foreign.Name, "foreign", posts.Public, false)
	if e := r.AddCollectionPosts(ctx, other, pub.Name, c.Slug, []string{a.ID}, false); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("collection hijack", e)
	}
	if e := r.AddCollectionPosts(ctx, editor, pub.Name, c.Slug, []string{a.ID, hidden.ID, alien.ID}, true); !errors.Is(e, interactions.ErrUnsupported) {
		t.Fatal("foreign collection policy", e)
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM owned_post_collection_items WHERE collection_id=$1`, c.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial batch", n, e)
	}
	if e := r.AddCollectionPosts(ctx, editor, pub.Name, c.Slug, []string{a.ID, hidden.ID, a.ID}, true); e != nil {
		t.Fatal(e)
	}
	if e := r.AddCollectionPosts(ctx, editor, pub.Name, c.Slug, []string{a.ID}, false); !errors.Is(e, interactions.ErrDuplicate) {
		t.Fatal(e)
	}
	if got, total, e := r.CollectionPosts(ctx, "", strings.ToUpper(pub.Name), "MIXED", interactions.Page{Take: 20}); e != nil || total != 1 || len(got) != 1 || got[0].ID != a.ID {
		t.Fatal("collection visibility count", got, total, e)
	}
	if got, total, e := r.CollectionPosts(ctx, editor, pub.Name, c.Slug, interactions.Page{Take: 20}); e != nil || total != 2 || len(got) != 2 {
		t.Fatal(got, total, e)
	}
	if got, e := p.Get(ctx, "", a.ID); e != nil || len(got.PublisherCollections) != 1 || got.PublisherCollections[0].ID != c.ID {
		t.Fatal("post missing linked collection", got, e)
	}
	if _, e := r.UpdateCollection(ctx, editor, pub.Name, c.Slug, interactions.CollectionRequest{Name: postString("renamed"), Description: postString("clear me")}); e != nil {
		t.Fatal(e)
	}
	if got, e := r.UpdateCollection(ctx, editor, pub.Name, c.Slug, interactions.CollectionRequest{Name: postString("replacement")}); e != nil || got.Description != nil {
		t.Fatal("source nullable replacement", got, e)
	}
	if e := r.RemoveCollectionPosts(ctx, editor, pub.Name, c.Slug, []string{a.ID}); e != nil {
		t.Fatal(e)
	}
	if e := r.DeleteCollection(ctx, other, pub.Name, c.Slug); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("foreign deletion", e)
	}
	if e := r.DeleteCollection(ctx, owner, pub.Name, c.Slug); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Collection(ctx, owner, pub.Name, c.Slug); !errors.Is(e, interactions.ErrCollectionNotFound) {
		t.Fatal("deleted collection", e)
	}
	if e := db.QueryRow(`SELECT count(*) FROM owned_post_collection_items WHERE collection_id=$1`, c.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("deleted collection links", n, e)
	}
}
func TestPGPostInteractionsCanonicalTaxonomyAndAtomicLinks(t *testing.T) {
	db := interactionDB(t)
	r, p := interactionRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2020)
	other, foreign := postPublisher(t, db, 2021)
	c := interactionCollection(t, r, owner, pub.Name, "with-tags")
	foreignC := interactionCollection(t, r, other, foreign.Name, "foreign-tags")
	categoryID := "d5200000-0000-4000-8000-000000002020"
	query(t, db, `INSERT INTO owned_taxonomy.post_categories(id,slug,created_at,updated_at)VALUES($1,'interaction-category',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, categoryID)
	got, e := p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("canonical links"), Tags: strList(" New-Tag ", "new-tag"), Categories: strList("interaction-category"), CollectionIDs: strList(c.ID)})
	if e != nil || len(got.Tags) != 1 || len(got.Categories) != 1 || len(got.PublisherCollections) != 1 {
		t.Fatal(got, e)
	}
	var linkPost string
	if e = db.QueryRow(`SELECT posts_id::text FROM owned_taxonomy.post_tag_links WHERE tags_id=$1`, got.Tags[0].ID).Scan(&linkPost); e != nil || linkPost != got.ID {
		t.Fatal("noncanonical link", linkPost, e)
	}
	tag, e := taxonomypg.NewWithIndependentPolicy(db, 0).Get(ctx, taxonomy.Tag, "new-tag", true)
	if e != nil || tag.Usage != 1 {
		t.Fatal("taxonomy usage misses actual post", tag, e)
	}
	if got, e = p.Update(ctx, owner, got.ID, posts.Patch{Title: postString("still tagged")}); e != nil || len(got.Tags) != 1 {
		t.Fatal("omitted tags lost", got, e)
	}
	if _, e = p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("rollback missing category"), Tags: strList("must-rollback-tag"), Categories: strList("missing-category")}); !errors.Is(e, posts.ErrInvalid) {
		t.Fatal(e)
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM owned_taxonomy.post_tags WHERE slug='must-rollback-tag'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("tag escaped transaction", n, e)
	}
	if _, e = p.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("rollback foreign collection"), CollectionIDs: strList(foreignC.ID)}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("foreign composer link", e)
	}
	query(t, db, `UPDATE owned_taxonomy.post_tags SET owner_publisher_id=$1,is_protected=true WHERE id=$2`, foreign.ID, got.Tags[0].ID)
	if _, e = p.Update(ctx, owner, got.ID, posts.Patch{Content: postString("must not change"), Tags: strList("new-tag")}); !errors.Is(e, interactions.ErrTagUnavailable) {
		t.Fatal("protected tag", e)
	}
	current, e := p.Get(ctx, owner, got.ID)
	if e != nil || current.Content != "canonical links" {
		t.Fatal("partial content update", current, e)
	}
	query(t, db, `UPDATE owned_taxonomy.post_tags SET is_protected=false,is_event=true,event_ends_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, got.Tags[0].ID)
	if _, e = p.Update(ctx, owner, got.ID, posts.Patch{Tags: strList("new-tag")}); !errors.Is(e, interactions.ErrTagUnavailable) {
		t.Fatal("expired event tag", e)
	}
	if current, e = p.Update(ctx, owner, got.ID, posts.Patch{Tags: strList(), Categories: strList()}); e != nil || len(current.Tags) != 0 || len(current.Categories) != 0 {
		t.Fatal("clear associations", current, e)
	}
}
func TestPGPostInteractionsIndependentPoolRaces(t *testing.T) {
	db := interactionDB(t)
	a, p := interactionRepo(db)
	b := interactionpg.New(database(t))
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2030)
	post := createPost(t, p, owner, pub.Name, "concurrent", posts.Public, false)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, r := range []*interactionpg.Repository{a, b} {
		wg.Add(1)
		go func(r *interactionpg.Repository) {
			defer wg.Done()
			<-start
			_, _, e := r.React(ctx, owner, post.ID, interactions.ReactionRequest{Symbol: "heart"})
			errs <- e
		}(r)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM owned_post_reactions WHERE post_id=$1`, post.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("double toggle not serialized", n, e)
	}
	start = make(chan struct{})
	bookmarks := make(chan interactions.Bookmark, 2)
	errs = make(chan error, 2)
	for _, r := range []*interactionpg.Repository{a, b} {
		wg.Add(1)
		go func(r *interactionpg.Repository) {
			defer wg.Done()
			<-start
			v, e := r.Bookmark(ctx, owner, post.ID)
			bookmarks <- v
			errs <- e
		}(r)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	one, two := <-bookmarks, <-bookmarks
	if one.ID == "" || one.ID != two.ID {
		t.Fatal("duplicate bookmarks", one, two)
	}
	if e := db.QueryRow(`SELECT count(*) FROM owned_post_interaction_audit WHERE post_id=$1 AND action='bookmark'`, post.ID).Scan(&n); e != nil || n != 1 {
		t.Fatal("duplicate idempotent audit", n, e)
	}
	// Different actors/publishers must still converge on one normalized tag.
	other, foreign := postPublisher(t, db, 2031)
	start = make(chan struct{})
	errs = make(chan error, 2)
	for _, pair := range []struct {
		actor, pub string
		repo       *postpg.Repository
	}{{owner, pub.Name, p}, {other, foreign.Name, postpg.NewWithAssociations(database(t), b)}} {
		wg.Add(1)
		go func(actor, pub string, r *postpg.Repository) {
			defer wg.Done()
			<-start
			_, e := r.Create(ctx, actor, pub, posts.Patch{Content: postString("shared topic"), Tags: strList("concurrent-topic")})
			errs <- e
		}(pair.actor, pair.pub, pair.repo)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if e := db.QueryRow(`SELECT count(*) FROM owned_taxonomy.post_tags WHERE slug='concurrent-topic'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("duplicate auto-created tag", n, e)
	}
}
func TestPGPostInteractionsAuditRollback(t *testing.T) {
	db := interactionDB(t)
	r, p := interactionRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2040)
	post := createPost(t, p, owner, pub.Name, "audit protected", posts.Public, false)
	c := interactionCollection(t, r, owner, pub.Name, "audit-test")
	query(t, db, `ALTER TABLE owned_post_interaction_audit ADD CONSTRAINT interaction_synthetic_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000002040'::uuid OR action='collection.create')`)
	t.Cleanup(func() {
		query(t, db, `ALTER TABLE owned_post_interaction_audit DROP CONSTRAINT IF EXISTS interaction_synthetic_failure`)
	})
	if _, _, e := r.React(ctx, owner, post.ID, interactions.ReactionRequest{Symbol: "heart"}); e == nil {
		t.Fatal("reaction audit failure hidden")
	}
	if _, e := r.Bookmark(ctx, owner, post.ID); e == nil {
		t.Fatal("bookmark audit failure hidden")
	}
	if e := r.AddCollectionPosts(ctx, owner, pub.Name, c.Slug, []string{post.ID}, false); e == nil {
		t.Fatal("collection audit failure hidden")
	}
	if e := r.DeleteCollection(ctx, owner, pub.Name, c.Slug); e == nil {
		t.Fatal("collection deletion audit failure hidden")
	}
	if _, e := p.Update(ctx, owner, post.ID, posts.Patch{Content: postString("rollback"), Tags: strList("rollback-audit-tag")}); e == nil {
		t.Fatal("association audit failure hidden")
	}
	var n int
	for _, q := range []string{`SELECT count(*) FROM owned_post_reactions WHERE post_id=$1`, `SELECT count(*) FROM owned_post_bookmarks WHERE post_id=$1`, `SELECT count(*) FROM owned_post_collection_items WHERE post_id=$1`} {
		if e := db.QueryRow(q, post.ID).Scan(&n); e != nil || n != 0 {
			t.Fatal("partial mutation", q, n, e)
		}
	}
	if got, e := p.Get(ctx, owner, post.ID); e != nil || got.Content != "audit protected" {
		t.Fatal(got, e)
	}
	if _, e := r.Collection(ctx, owner, pub.Name, c.Slug); e != nil {
		t.Fatal("delete not rolled back", e)
	}
	if e := db.QueryRow(`SELECT count(*) FROM owned_taxonomy.post_tags WHERE slug='rollback-audit-tag'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("orphan auto tag", n, e)
	}
}
func interactionAuthority(t *testing.T, db *sql.DB, actor string) (*authority.Service, *authority.Verifier, *authoritypg.TransactionGuard, string) {
	t.Helper()
	keys := authorityKeys(t)
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, keys, now)
	service, e := authority.New(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	pair := pgIssue(t, service, actor)
	return service, v, authoritypg.NewTransactionGuard(authoritypg.New(db), v), pair.Token
}
func TestPGPostInteractionsAuthenticatedHTTPAndRevocation(t *testing.T) {
	db := interactionDB(t)
	_, p := interactionRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2050)
	post := createPost(t, p, owner, pub.Name, "http interactions", posts.Public, false)
	service, v, guard, token := interactionAuthority(t, db, owner)
	r := interactionpg.NewWithAuthority(db, guard)
	h := interactions.NewVerifiedHandler(r, v)
	if w := postHTTP(h, "POST", "/api/posts/"+post.ID+"/reactions", `{"symbol":"heart","attitude":0}`, token); w.Code != 403 {
		t.Fatal("missing grant", w.Code, w.Body.String())
	}
	if e := authoritypg.New(db).ReplacePolicy(ctx, owner, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	if w := postHTTP(h, "POST", "/api/posts/"+post.ID+"/reactions", `{"symbol":"heart","attitude":0}`, token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postHTTP(h, "GET", "/api/posts/"+post.ID+"/bookmark", "", token); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "null" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postHTTP(h, "POST", "/api/publishers/"+pub.Name+"/collections", `{"slug":"http-collection"}`, token); w.Code != 201 || !strings.HasSuffix(w.Header().Get("Location"), "/http-collection") {
		t.Fatal(w.Code, w.Body.String())
	}
	req := httptest.NewRequest("POST", "/api/posts/"+post.ID+"/bookmark", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	proof, e := v.AuthenticateRequest(req)
	if e != nil {
		t.Fatal(e)
	}
	// Preverified proof cannot survive a revocation committed while it waits for
	// the exact canonical authority row held by the domain transaction guard.
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, owner); e != nil {
		t.Fatal(e)
	}
	waiting, cancel := context.WithTimeout(guard.Bind(ctx, proof), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := r.Bookmark(waiting, owner, post.ID); done <- e }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if e = db.QueryRowContext(waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%owned_authority_accounts%' AND query LIKE '%FOR UPDATE%')`).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case <-waiting.Done():
			t.Fatal("guard did not wait")
		case <-ticker.C:
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE account_id=$1`, owner); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("revoked proof mutated bookmark")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM owned_post_bookmarks WHERE account_id=$1`, owner).Scan(&n); e != nil || n != 0 {
		t.Fatal("revoked bookmark persisted", n, e)
	}
	if w := postHTTP(h, "GET", "/api/posts/"+post.ID+"/bookmark", "", token); w.Code != 401 {
		t.Fatal("revoked HTTP session", w.Code)
	}
	_ = service
}
func TestPGPostInteractionsRestartPersistence(t *testing.T) {
	if os.Getenv("PG_TEST_PHASE") == "after" {
		// Strict read-only branch: no migrations, seeds, tokens, clock updates or writes.
		db := database(t)
		r, p := interactionRepo(db)
		ctx := context.Background()
		actor := "d5100000-0000-4000-8000-000000002090"
		got, e := p.GetSlug(ctx, actor, "post-publisher-2090", "interactions-restart")
		if e != nil || got.Content != "persisted interactions" || got.ReactionsCount["heart"] != 1 || !got.ReactionsMade["heart"] || len(got.Tags) != 1 || len(got.PublisherCollections) != 1 {
			t.Fatal("restart post associations", got, e)
		}
		if saved, n, e := r.Bookmarks(ctx, actor, interactions.Page{Take: 20}); e != nil || n != 1 || len(saved) != 1 || saved[0].ID != got.ID {
			t.Fatal("restart bookmarks", saved, n, e)
		}
		if list, n, e := r.CollectionPosts(ctx, "", "post-publisher-2090", "restart-collection", interactions.Page{Take: 20}); e != nil || n != 1 || len(list) != 1 || list[0].ID != got.ID {
			t.Fatal("restart collection", list, n, e)
		}
		var n int
		if e = db.QueryRow(`SELECT count(*) FROM owned_post_interaction_audit WHERE actor_id=$1`, actor).Scan(&n); e != nil || n != 4 {
			t.Fatal("restart audits", n, e)
		}
		return
	}
	db := interactionDB(t)
	r, p := interactionRepo(db)
	ctx := context.Background()
	actor, pub := postPublisher(t, db, 2090)
	c := interactionCollection(t, r, actor, pub.Name, "restart-collection")
	got, e := p.Create(ctx, actor, pub.Name, posts.Patch{Content: postString("persisted interactions"), Slug: postString("interactions-restart"), Tags: strList("restart-interaction-tag"), CollectionIDs: strList(c.ID)})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = r.React(ctx, actor, got.ID, interactions.ReactionRequest{Symbol: "heart"}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Bookmark(ctx, actor, got.ID); e != nil {
		t.Fatal(e)
	}
}

func TestPGPostInteractionsMembershipRevocation(t *testing.T) {
	db := interactionDB(t)
	r, p := interactionRepo(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, pub := postPublisher(t, db, 2060)
	editor := postAccount(t, db, 2061)
	joinPostEditor(t, db, owner, editor, pub.ID)
	post := createPost(t, p, owner, pub.Name, "membership race", posts.Public, false)
	c := interactionCollection(t, r, owner, pub.Name, "member-race")
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, pub.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- r.AddCollectionPosts(ctx, editor, pub.Name, c.Slug, []string{post.ID}, false) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if e = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%owned_publishers%' AND query LIKE '%FOR UPDATE%')`).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("collection mutation did not wait")
		case <-ticker.C:
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=CURRENT_TIMESTAMP WHERE publisher_id=$1 AND account_id=$2`, pub.ID, editor); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("stale collection editor", e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_post_collection_items WHERE collection_id=$1`, c.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("revoked insertion persisted", count, e)
	}
}

func TestPGPostInteractionsDeletedAndDisabledIsolation(t *testing.T) {
	db := interactionDB(t)
	r, p := interactionRepo(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2070)
	viewer := postAccount(t, db, 2071)
	post := createPost(t, p, owner, pub.Name, "must disappear", posts.Public, false)
	c := interactionCollection(t, r, owner, pub.Name, "deleted")
	if e := r.AddCollectionPosts(ctx, owner, pub.Name, c.Slug, []string{post.ID}, false); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Bookmark(ctx, viewer, post.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e := r.React(ctx, viewer, post.ID, interactions.ReactionRequest{Symbol: "heart"}); e != nil {
		t.Fatal(e)
	}
	query(t, db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, owner)
	if _, _, e := r.Reactions(ctx, viewer, post.ID, interactions.Page{Take: 20}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("disabled owner reactions", e)
	}
	if got, n, e := r.Bookmarks(ctx, viewer, interactions.Page{Take: 20}); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("disabled owner bookmarks", got, n, e)
	}
	if _, e := r.Collection(ctx, viewer, pub.Name, c.Slug); !errors.Is(e, interactions.ErrCollectionNotFound) {
		t.Fatal("disabled owner collection", e)
	}
	query(t, db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, owner)
	if e := p.Delete(ctx, owner, post.ID); e != nil {
		t.Fatal(e)
	}
	if got, n, e := r.CollectionPosts(ctx, "", pub.Name, c.Slug, interactions.Page{Take: 20}); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("deleted post collection leak", got, n, e)
	}
	if got, n, e := r.Bookmarks(ctx, viewer, interactions.Page{Take: 20}); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("deleted post bookmark leak", got, n, e)
	}
	if _, _, e := r.Reactions(ctx, "", post.ID, interactions.Page{Take: 20}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted post reactions", e)
	}
	if e := r.Unbookmark(ctx, viewer, post.ID); e != nil {
		t.Fatal("deleted post bookmark cleanup", e)
	}
	query(t, db, `UPDATE owned_publishers SET deleted_at=CURRENT_TIMESTAMP WHERE id=$1`, pub.ID)
	if _, e := r.Collection(ctx, viewer, pub.Name, c.Slug); !errors.Is(e, interactions.ErrCollectionNotFound) {
		t.Fatal("deleted publisher collection", e)
	}
}

func TestPGPostInteractionsSocialVisibility(t *testing.T) {
	db := interactionDB(t)
	socialRepo := socialpg.New(db)
	r := interactionpg.New(db).WithVisibility(socialRepo)
	p := postpg.NewWithAssociations(db, r).WithVisibility(socialRepo)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 2080)
	viewer := postAccount(t, db, 2081)
	stranger := postAccount(t, db, 2082)
	public := createPost(t, p, owner, pub.Name, "public social", posts.Public, false)
	friends := createPost(t, p, owner, pub.Name, "friends social", posts.Friends, false)
	close := createPost(t, p, owner, pub.Name, "close social", posts.CloseFriends, false)
	c := interactionCollection(t, r, owner, pub.Name, "social-visibility")
	if e := r.AddCollectionPosts(ctx, owner, pub.Name, c.Slug, []string{public.ID, friends.ID, close.ID}, true); e != nil {
		t.Fatal(e)
	}
	for _, actor := range []string{"", viewer, stranger} {
		if got, n, e := p.List(ctx, actor, posts.ListOptions{Publisher: pub.Name, Take: 20}); e != nil || n != 1 || len(got) != 1 || got[0].ID != public.ID {
			t.Fatal("friendship required before count", actor, got, n, e)
		}
	}
	if _, e := socialRepo.ChangeRelationship(ctx, viewer, owner, social.RelationshipChange{Action: social.RequestFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Get(ctx, viewer, friends.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("pending friendship revealed body", e)
	}
	if _, e := socialRepo.ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
		t.Fatal(e)
	}
	if got, n, e := p.List(ctx, viewer, posts.ListOptions{Publisher: pub.Name, Take: 20}); e != nil || n != 2 || len(got) != 2 {
		t.Fatal("accepted friendship", got, n, e)
	}
	if _, e := p.Get(ctx, viewer, close.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("regular friend saw close content", e)
	}
	if _, e := socialRepo.ChangeRelationship(ctx, viewer, owner, social.RelationshipChange{Action: social.SetClose}); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Get(ctx, viewer, close.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("viewer self-selected close access", e)
	}
	if _, e := socialRepo.ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.SetClose}); e != nil {
		t.Fatal(e)
	}
	if got, n, e := r.CollectionPosts(ctx, viewer, pub.Name, c.Slug, interactions.Page{Take: 20}); e != nil || n != 3 || len(got) != 3 {
		t.Fatal("owner-directed close collection visibility", got, n, e)
	}
	if _, e := r.Bookmark(ctx, viewer, close.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e := r.React(ctx, viewer, close.ID, interactions.ReactionRequest{Symbol: "heart"}); e != nil {
		t.Fatal(e)
	}
	if _, e := socialRepo.ChangeRelationship(ctx, owner, viewer, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{public.ID, friends.ID, close.ID} {
		if _, e := p.Get(ctx, viewer, id); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("bilateral block detail leak", id, e)
		}
		if _, _, e := r.Reactions(ctx, viewer, id, interactions.Page{Take: 20}); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("bilateral block reaction leak", id, e)
		}
	}
	if got, n, e := p.List(ctx, viewer, posts.ListOptions{Publisher: pub.Name, Take: 20}); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("block count leak", got, n, e)
	}
	if got, n, e := r.CollectionPosts(ctx, viewer, pub.Name, c.Slug, interactions.Page{Take: 20}); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("block collection leak", got, n, e)
	}
	if got, n, e := r.Bookmarks(ctx, viewer, interactions.Page{Take: 20}); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("block bookmark leak", got, n, e)
	}
	if _, _, e := r.React(ctx, viewer, public.ID, interactions.ReactionRequest{Symbol: "heart"}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("blocked reaction allowed", e)
	}
	if _, e := socialRepo.ChangeRelationship(ctx, viewer, owner, social.RelationshipChange{Action: social.Forget}); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Get(ctx, viewer, public.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("viewer removed owner's block", e)
	}
	if _, e := p.Get(ctx, "", public.ID); e != nil {
		t.Fatal("public anonymous view", e)
	}
}
