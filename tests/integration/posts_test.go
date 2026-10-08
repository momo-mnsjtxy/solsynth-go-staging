// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func postDB(t *testing.T) *sql.DB {
	t.Helper()
	db := database(t)
	query(t, db, authoritypg.Schema)
	query(t, db, accountpg.Schema)
	query(t, db, postpg.Schema)
	return db
}
func postString(s string) *string { return &s }
func postInt(n int) *int          { return &n }
func postAccount(t *testing.T, db *sql.DB, n int) string {
	t.Helper()
	id := fmt.Sprintf("d5100000-0000-4000-8000-%012d", n)
	query(t, db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at) VALUES($1,$2,'Post fixture',CURRENT_TIMESTAMP)`, id, fmt.Sprintf("post-account-%d", n))
	return id
}
func postPublisher(t *testing.T, db *sql.DB, n int) (string, accounts.Publisher) {
	t.Helper()
	actor := postAccount(t, db, n)
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,3,CURRENT_TIMESTAMP)`, actor)
	p, e := accountpg.NewForCompatibilityTests(db).CreatePublisher(context.Background(), actor, accounts.PublisherPatch{Name: postString(fmt.Sprintf("post-publisher-%d", n)), Nick: postString("Post publisher")})
	if e != nil {
		t.Fatal(e)
	}
	return actor, p
}
func createPost(t *testing.T, r *postpg.Repository, actor, publisher, body string, visibility int, draft bool) posts.Post {
	t.Helper()
	v := posts.Patch{Content: postString(body), Visibility: &visibility}
	if draft {
		now := time.Now().UTC()
		v.DraftedAt = &now
	}
	p, e := r.Create(context.Background(), actor, publisher, v)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func joinPostEditor(t *testing.T, db *sql.DB, owner, editor, publisher string) {
	t.Helper()
	r := accountpg.NewForCompatibilityTests(db)
	_, e := r.ChangeMember(context.Background(), owner, publisher, accounts.MemberChange{Action: accounts.InviteMember, AccountID: editor, Role: accounts.Editor})
	if e != nil {
		t.Fatal(e)
	}
	_, e = r.ChangeMember(context.Background(), editor, publisher, accounts.MemberChange{Action: accounts.AcceptInvite})
	if e != nil {
		t.Fatal(e)
	}
}
func TestPGPostsLifecycleAndPrivacy(t *testing.T) {
	db := postDB(t)
	r := postpg.New(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 1)
	other := postAccount(t, db, 2)
	editor := postAccount(t, db, 3)
	public := createPost(t, r, owner, pub.Name, "public body", posts.Public, false)
	private := createPost(t, r, owner, pub.Name, "private body", posts.Private, false)
	unlisted := createPost(t, r, owner, pub.Name, "unlisted body", posts.Unlisted, false)
	draft := createPost(t, r, owner, pub.Name, "draft body", posts.Public, true)
	if public.PublisherID == owner || public.Publisher.ID != pub.ID {
		t.Fatal("invented publisher identity", public)
	}
	for _, actor := range []string{"", other} {
		for _, p := range []posts.Post{private, draft} {
			if _, e := r.Get(ctx, actor, p.ID); !errors.Is(e, posts.ErrNotFound) {
				t.Fatal("private/draft detail leak", actor, e)
			}
		}
		if p, e := r.Get(ctx, actor, unlisted.ID); e != nil || p.Content != "unlisted body" {
			t.Fatal("unlisted detail", e)
		}
		list, total, e := r.List(ctx, actor, posts.ListOptions{Publisher: pub.Name, Take: 20})
		if e != nil || len(list) != 1 || total != 1 || list[0].ID != public.ID {
			t.Fatal("private/unlisted count leak", list, total, e)
		}
	}
	if _, e := r.Update(ctx, other, public.ID, posts.Patch{Content: postString("hijack")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("cross-account update", e)
	}
	if e := r.Delete(ctx, other, public.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("cross-account delete", e)
	}
	if _, e := r.Publish(ctx, other, draft.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("cross-account publish", e)
	}
	if _, _, e := r.List(ctx, other, posts.ListOptions{Drafts: true, Publisher: pub.Name, Take: 20}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("cross-account drafts", e)
	}
	if list, total, e := r.List(ctx, owner, posts.ListOptions{Drafts: true, Take: 1}); e != nil || total != 1 || len(list) != 1 || list[0].ID != draft.ID {
		t.Fatal(list, total, e)
	}
	if _, e := accountpg.NewForCompatibilityTests(db).ChangeMember(ctx, owner, pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: editor, Role: accounts.Editor}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Get(ctx, editor, draft.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("pending invitation read", e)
	}
	if _, e := r.Update(ctx, editor, public.ID, posts.Patch{Title: postString("pending")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("pending invitation write", e)
	}
	if _, e := accountpg.NewForCompatibilityTests(db).ChangeMember(ctx, editor, pub.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Get(ctx, editor, private.ID); e != nil {
		t.Fatal("accepted editor private read", e)
	}
	p, e := r.Update(ctx, editor, public.ID, posts.Patch{Title: postString(" Edited "), Slug: postString("Mixed-Slug"), Type: postInt(posts.Article)})
	if e != nil || *p.Title != "Edited" || p.Content != "public body" || p.Type != posts.Article {
		t.Fatal(p, e)
	}
	if p, e = postpg.New(database(t)).GetSlug(ctx, "", strings.ToUpper(pub.Name), "mixed-slug"); e != nil || p.ID != public.ID {
		t.Fatal("independent pool slug read", p, e)
	}
	if p, e = r.Publish(ctx, editor, draft.ID); e != nil || p.DraftedAt != nil || p.PublishedAt == nil {
		t.Fatal(p, e)
	}
	if _, e = r.Publish(ctx, editor, draft.ID); !errors.Is(e, posts.ErrNotDraft) {
		t.Fatal("republish", e)
	}
	if list, total, e := r.List(ctx, "", posts.ListOptions{Publisher: pub.Name, Take: 1}); e != nil || total != 2 || len(list) != 1 {
		t.Fatal(list, total, e)
	}
	if e = r.Delete(ctx, owner, public.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, owner, public.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted read", e)
	}
	if _, e = r.GetSlug(ctx, "", pub.Name, "Mixed-Slug"); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted slug", e)
	}
	var deleted bool
	var audits int
	if e = db.QueryRow(`SELECT deleted_at IS NOT NULL FROM owned_text_posts WHERE id=$1`, public.ID).Scan(&deleted); e != nil || !deleted {
		t.Fatal("not soft deleted", e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, public.ID).Scan(&audits); e != nil || audits != 3 {
		t.Fatal("mutation audits", audits, e)
	}
	if e = accountpg.NewForCompatibilityTests(db).DeletePublisher(ctx, owner, pub.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, "", draft.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted publisher read", e)
	}
}
func TestPGPostsAtomicRollbackAndCancellation(t *testing.T) {
	db := postDB(t)
	r := postpg.New(db)
	ctx := context.Background()
	owner, pub := postPublisher(t, db, 10)
	p := createPost(t, r, owner, pub.Name, "unchanged", posts.Private, false)
	query(t, db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT posts_synthetic_audit_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000000010'::uuid OR action='create')`)
	t.Cleanup(func() {
		query(t, db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT IF EXISTS posts_synthetic_audit_failure`)
	})
	if _, e := r.Update(ctx, owner, p.ID, posts.Patch{Content: postString("must rollback")}); e == nil {
		t.Fatal("audit failure hidden")
	}
	if e := r.Delete(ctx, owner, p.ID); e == nil {
		t.Fatal("audit deletion failure hidden")
	}
	if got, e := r.Get(ctx, owner, p.ID); e != nil || got.Content != "unchanged" || got.DeletedAt != nil {
		t.Fatal("partial write", got, e)
	}
	query(t, db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT posts_synthetic_audit_failure`)
	if _, e := r.Update(ctx, owner, p.ID, posts.Patch{Slug: postString("reserved")}); e != nil {
		t.Fatal(e)
	}
	if e := r.Delete(ctx, owner, p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("duplicate"), Slug: postString("RESERVED")}); !errors.Is(e, posts.ErrConflict) {
		t.Fatal("deleted slug takeover", e)
	}
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, pub.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("failed insert persisted", count, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := r.Create(cancelled, owner, pub.Name, posts.Patch{Content: postString("cancelled")}); e == nil {
		t.Fatal("cancelled write")
	}
}

func TestPGPostsVisibilityAndStateGuards(t *testing.T) {
	db := postDB(t)
	owner, pub := postPublisher(t, db, 15)
	r := postpg.New(db)
	ctx := context.Background()
	for _, visibility := range []int{posts.Friends, posts.CloseFriends, posts.QuietPublic, -1, 6} {
		if _, e := r.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("unsupported"), Visibility: &visibility}); !errors.Is(e, posts.ErrUnsupported) {
			t.Fatal(visibility, e)
		}
	}
	future := time.Now().Add(time.Hour)
	if _, e := r.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("scheduled"), PublishedAt: &future}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal(e)
	}
	if _, e := r.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("blog"), Type: postInt(posts.Blog)}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal(e)
	}
	p := createPost(t, r, owner, pub.Name, "locked draft", posts.Private, true)
	query(t, db, `UPDATE owned_text_posts SET locked_at=CURRENT_TIMESTAMP WHERE id=$1`, p.ID)
	if _, e := r.Update(ctx, owner, p.ID, posts.Patch{Content: postString("edit")}); !errors.Is(e, posts.ErrLocked) {
		t.Fatal("locked update", e)
	}
	if _, e := r.Publish(ctx, owner, p.ID); !errors.Is(e, posts.ErrLocked) {
		t.Fatal("locked publish", e)
	}
	if e := r.Delete(ctx, owner, p.ID); !errors.Is(e, posts.ErrLocked) {
		t.Fatal("locked delete", e)
	}
	query(t, db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, owner)
	if _, e := r.Get(ctx, owner, p.ID); !errors.Is(e, posts.ErrUnauthenticated) {
		t.Fatal("disabled actor", e)
	}
	if _, e := r.Get(ctx, "", p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("disabled owner", e)
	}
	if _, e := r.Create(ctx, owner, pub.Name, posts.Patch{Content: postString("disabled")}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("disabled create", e)
	}
}
func TestPGPostsIndependentPoolConcurrency(t *testing.T) {
	db := postDB(t)
	owner, pub := postPublisher(t, db, 20)
	editor := postAccount(t, db, 21)
	joinPostEditor(t, db, owner, editor, pub.ID)
	a, b := postpg.New(db), postpg.New(database(t))
	ctx := context.Background()
	p := createPost(t, a, owner, pub.Name, "before", posts.Public, true)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, r := range []*postpg.Repository{a, b} {
		wg.Add(1)
		go func(i int, r *postpg.Repository) {
			defer wg.Done()
			<-start
			patch := posts.Patch{Title: postString("after title")}
			actor := owner
			if i == 1 {
				patch = posts.Patch{Content: postString("after content")}
				actor = editor
			}
			_, e := r.Update(ctx, actor, p.ID, patch)
			errs <- e
		}(i, r)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	got, e := a.Get(ctx, owner, p.ID)
	if e != nil || got.Title == nil || *got.Title != "after title" || got.Content != "after content" {
		t.Fatal("lost patch", got, e)
	}
	start = make(chan struct{})
	errs = make(chan error, 2)
	for i, r := range []*postpg.Repository{a, b} {
		wg.Add(1)
		go func(i int, r *postpg.Repository) {
			defer wg.Done()
			<-start
			actor := owner
			if i == 1 {
				actor = editor
			}
			_, e := r.Publish(ctx, actor, p.ID)
			errs <- e
		}(i, r)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, notDraft := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, posts.ErrNotDraft) {
			notDraft++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || notDraft != 1 {
		t.Fatal("double publication", success, notDraft)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1 AND action='publish'`, p.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate publish audit", count, e)
	}
}
func TestPGPostsMembershipRevocationSerializesWithWrites(t *testing.T) {
	db := postDB(t)
	owner, pub := postPublisher(t, db, 30)
	editor := postAccount(t, db, 31)
	joinPostEditor(t, db, owner, editor, pub.ID)
	r := postpg.New(database(t))
	p := createPost(t, r, owner, pub.Name, "retained", posts.Private, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, pub.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Update(ctx, editor, p.ID, posts.Patch{Content: postString("revoked")}); done <- e }()
	// Observe the publisher lock wait, then revoke while still owning that lock.
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
			t.Fatal("post did not wait for publisher lock")
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
		t.Fatal("stale editor authorization", e)
	}
	if got, e := r.Get(ctx, owner, p.ID); e != nil || got.Content != "retained" {
		t.Fatal(got, e)
	}
	if _, e = r.Get(ctx, editor, p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("revoked private read", e)
	}
}
func postAuthHandler(t *testing.T, db *sql.DB, actor string) (http.Handler, string) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	// Ephemeral synthetic fixture values only; never printed or used externally.
	hash, seal := make([]byte, 32), make([]byte, 32)
	if _, e = rand.Read(hash); e != nil {
		t.Fatal(e)
	}
	if _, e = rand.Read(seal); e != nil {
		t.Fatal(e)
	}
	cfg := authority.Config{Issuer: "https://posts.invalid", Audience: "posts-fixture", SigningKeyID: "fixture", SigningKey: key, TrustedKeys: map[string]*rsa.PublicKey{"fixture": &key.PublicKey}, RefreshHashKey: hash, ReplaySealKey: seal}
	service, e := authority.New(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	pair, e := service.IssueSession(context.Background(), actor, authority.SessionOptions{Label: "post fixture", Scopes: []string{"posts.*"}})
	if e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	return posts.NewVerifiedHandler(postpg.New(db), v), pair.Token
}
func postHTTP(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestPGPostsAuthenticatedHTTPAndFreshAuthority(t *testing.T) {
	db := postDB(t)
	actor, pub := postPublisher(t, db, 40)
	h, token := postAuthHandler(t, db, actor)
	body := `{"content":"authenticated text","visibility":3,"type":1}`
	path := "/api/posts?pub=" + pub.Name
	if w := postHTTP(h, "POST", path, body, ""); w.Code != 401 {
		t.Fatal("anonymous mutation", w.Code, w.Body.String())
	}
	if w := postHTTP(h, "POST", path, body, token); w.Code != 403 {
		t.Fatal("missing grant", w.Code, w.Body.String())
	}
	query(t, db, `INSERT INTO owned_authority_grants(account_id,pattern)VALUES($1,'posts.*')`, actor)
	w := postHTTP(h, "POST", path, body, token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p posts.Post
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if w = postHTTP(h, "GET", "/api/posts/"+p.ID, "", token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = postHTTP(h, "GET", "/api/posts/"+p.ID, "", ""); w.Code != 404 || strings.Contains(w.Body.String(), "authenticated text") {
		t.Fatal("private HTTP leak", w.Code, w.Body.String())
	}
	query(t, db, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1,'posts.update',true)`, actor)
	if w = postHTTP(h, "PATCH", "/api/posts/"+p.ID, `{"content":"denied"}`, token); w.Code != 403 {
		t.Fatal("stale grant", w.Code, w.Body.String())
	}
	query(t, db, `UPDATE owned_authority_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE account_id=$1`, actor)
	if w = postHTTP(h, "GET", "/api/posts/"+p.ID, "", token); w.Code != 401 {
		t.Fatal("stale session", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "/api/posts/"+p.ID, nil)
	r.Header.Set("X-Account-ID", actor)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("spoofed identity", w.Code)
	}
}
func TestPGPostsRestartPersistence(t *testing.T) {
	if os.Getenv("PG_TEST_PHASE") == "after" {
		// Deliberately no migrations, seeds or writes in the restart phase.
		db := database(t)
		r := postpg.New(db)
		p, e := r.GetSlug(context.Background(), "", "post-publisher-90", "restart-sentinel")
		if e != nil || p.Content != "post persisted across PostgreSQL restart" || p.Type != posts.Article {
			t.Fatal("restart sentinel missing", p, e)
		}
		var n int
		if e = db.QueryRow(`SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, p.ID).Scan(&n); e != nil || n != 2 {
			t.Fatal("restart audit missing", n, e)
		}
		return
	}
	db := postDB(t)
	owner, pub := postPublisher(t, db, 90)
	r := postpg.New(db)
	now := time.Now().UTC()
	p, e := r.Create(context.Background(), owner, pub.Name, posts.Patch{Content: postString("post persisted across PostgreSQL restart"), Slug: postString("restart-sentinel"), Type: postInt(posts.Article), DraftedAt: &now})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Publish(context.Background(), owner, p.ID); e != nil {
		t.Fatal(e)
	}
}
