// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/blogpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/reader"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

type blogVerifyFunc func(context.Context, string, string) (bool, error)

func (f blogVerifyFunc) VerifyDomain(c context.Context, d, p string) (bool, error) { return f(c, d, p) }

type blogPreviewFunc func(context.Context, string) (reader.Link, error)

func (f blogPreviewFunc) Preview(c context.Context, u string) (reader.Link, error) { return f(c, u) }

type blogFixture struct {
	postMediaFixture
	domains                 *blogpg.Repository
	configBlog              blogpg.Config
	previews, verifications *atomic.Int32
}

func newBlogFixture(t *testing.T) blogFixture {
	t.Helper()
	f := blogFixture{postMediaFixture: newPostSchedulingFixture(t), previews: &atomic.Int32{}, verifications: &atomic.Int32{}}
	query(t, f.db, blogpg.Schema)
	f.configBlog = blogpg.Config{Now: f.config.Now, Verifier: blogVerifyFunc(func(_ context.Context, d, p string) (bool, error) {
		f.verifications.Add(1)
		return d == "blog.example" && p == f.pub.Name, nil
	}), Previewer: blogPreviewFunc(func(_ context.Context, u string) (reader.Link, error) {
		f.previews.Add(1)
		desc := "Synthetic description"
		return reader.Link{URL: u, Title: "Synthetic blog title", Description: &desc}, nil
	})}
	return blogPool(t, f)
}
func blogPool(t *testing.T, f blogFixture) blogFixture {
	t.Helper()
	var e error
	f.domains, e = blogpg.New(f.db, f.guard, f.configBlog)
	if e != nil {
		t.Fatal(e)
	}
	f.posts.WithBlogs(f.domains)
	if e = f.domains.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func verifiedBlogDomain(t *testing.T, f blogFixture) blogs.Domain {
	t.Helper()
	v, e := f.domains.Add(f.ctx, f.actor, f.pub.Name, " BLOG.EXAMPLE ")
	if e != nil || v.Status != blogs.Verified || v.VerifiedAt == nil || v.FailedAttempts != 0 {
		t.Fatal(v, e)
	}
	return v
}
func createBlog(t *testing.T, f blogFixture, v posts.Patch) posts.Post {
	t.Helper()
	kind := posts.Blog
	v.Type = &kind
	if v.Content == nil {
		v.Content = postString("https://blog.example/article")
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, v)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestPGBlogDomainLifecyclePermissionsAndProof(t *testing.T) {
	f := newBlogFixture(t)
	before, e := f.posts.CheckBlogPermission(f.ctx, f.actor, f.pub.Name, "https://BLOG.EXAMPLE/article")
	if e != nil || before.CanCreate || before.DomainVerified || !before.PermissionGranted || !before.PermissionNodeGranted || before.Domain != "blog.example" {
		t.Fatal(before, e)
	}
	_, e = f.domains.List(f.otherCtx, f.other, f.pub.Name)
	if !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("non-editor domain disclosure", e)
	}
	v := verifiedBlogDomain(t, f)
	if _, e = f.domains.Add(f.ctx, f.actor, f.pub.Name, "blog.example"); !errors.Is(e, blogs.ErrConflict) {
		t.Fatal("duplicate domain", e)
	}
	got, e := f.domains.List(f.ctx, f.actor, f.pub.ID)
	if e != nil || len(got) != 1 || got[0].ID != v.ID {
		t.Fatal(got, e)
	}
	p, e := f.posts.CheckBlogPermission(f.ctx, f.actor, "", "https://blog.example/article")
	if e != nil || !p.CanCreate || p.PublisherID != f.pub.ID {
		t.Fatal(p, e)
	}
	query(t, f.db, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1,'posts.create.blog',true)`, f.actor)
	p, e = f.posts.CheckBlogPermission(f.ctx, f.actor, f.pub.Name, "https://blog.example/article")
	if e != nil || p.CanCreate || p.PermissionGranted || p.PermissionNodeGranted || !p.DomainVerified {
		t.Fatal(p, e)
	}
	if _, e = f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Type: postInt(posts.Blog), Content: postString("https://blog.example/article")}); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal(e)
	}
	if f.previews.Load() != 0 {
		t.Fatal("denied blog attempted preview")
	}
	query(t, f.db, `DELETE FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create.blog'`, f.actor)
	f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) { return false, nil })
	f = blogPool(t, f)
	failed, e := f.domains.Recheck(f.ctx, f.actor, f.pub.Name, v.ID)
	if e != nil || failed.Status != blogs.Failed || failed.LastError == nil || failed.FailedAttempts != 1 || failed.Revision != 2 {
		t.Fatal(failed, e)
	}
	if p, e = f.posts.CheckBlogPermission(f.ctx, f.actor, f.pub.Name, "https://blog.example/article"); e != nil || p.DomainVerified {
		t.Fatal(p, e)
	}
	if e = f.domains.Remove(f.ctx, f.actor, f.pub.Name, v.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.domains.Recheck(f.ctx, f.actor, f.pub.Name, v.ID); !errors.Is(e, blogs.ErrNotFound) {
		t.Fatal(e)
	}
}

func TestPGBlogCreationEditingMediaAndVisibility(t *testing.T) {
	f := newBlogFixture(t)
	verifiedBlogDomain(t, f)
	file := privatePostFile(t, f.postMediaFixture, "blog", "synthetic private blog file")
	private := posts.Private
	p := createBlog(t, f, posts.Patch{Visibility: &private, Attachments: mediaIDs(file.ID)})
	if p.Meta == nil || len(p.Meta.Embeds) != 1 || p.Meta.Embeds[0].Type != "link" || p.Title == nil || *p.Title != "Synthetic blog title" || p.Description == nil || len(p.Attachments) != 1 {
		t.Fatal(p)
	}
	if _, e := f.posts.Get(context.Background(), "", p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private blog exposed", e)
	}
	missingPostFile(t, f.postMediaFixture, context.Background(), "", file.ID)
	custom := "Author title"
	changed := "https://blog.example/revised"
	p, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Title: &custom, Content: &changed})
	if e != nil || *p.Title != custom || p.Meta.Embeds[0].URL != changed || len(p.Attachments) != 1 {
		t.Fatal(p, e)
	}
	p, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: postInt(posts.Public)})
	if e != nil {
		t.Fatal(e)
	}
	read, e := f.posts.Get(context.Background(), "", p.ID)
	if e != nil || read.Meta.Embeds[0].URL != changed {
		t.Fatal(read, e)
	}
	mediaRead(t, f.repo, context.Background(), "", file.ID, "synthetic private blog file")
	feed := timelinepg.New(f.db).WithVisibility(socialpg.New(f.db))
	found, total, e := feed.Search(context.Background(), "", timeline.SearchOptions{Take: 20, Type: postInt(posts.Blog), Publisher: f.pub.Name})
	if e != nil || total != 1 || len(found) != 1 || found[0].ID != p.ID || found[0].Meta == nil {
		t.Fatal(found, total, e)
	}
	article := posts.Article
	p, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Type: &article, Content: postString("Converted article")})
	if e != nil || p.Meta != nil {
		t.Fatal(p, e)
	}
}
func TestPGBlogPreviewFailureAndRevocationNeverCommit(t *testing.T) {
	f := newBlogFixture(t)
	domain := verifiedBlogDomain(t, f)
	f.configBlog.Previewer = blogPreviewFunc(func(context.Context, string) (reader.Link, error) {
		return reader.Link{}, errors.New("synthetic fetch failed")
	})
	f = blogPool(t, f)
	if _, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Type: postInt(posts.Blog), Content: postString("https://blog.example/article")}); !errors.Is(e, blogs.ErrPreview) {
		t.Fatal(e)
	}
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.configBlog.Previewer = blogPreviewFunc(func(ctx context.Context, u string) (reader.Link, error) {
		close(entered)
		select {
		case <-release:
			return reader.Link{URL: u, Title: "Synthetic"}, nil
		case <-ctx.Done():
			return reader.Link{}, ctx.Err()
		}
	})
	f = blogPool(t, f)
	done := make(chan error, 1)
	go func() {
		_, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Type: postInt(posts.Blog), Content: postString("https://blog.example/article")})
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preview did not start")
	}
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	if e := f.domains.Remove(ctx, f.actor, f.pub.ID, domain.ID); e != nil {
		close(release)
		t.Fatal("preview retained locks", e)
	}
	close(release)
	if e := <-done; !errors.Is(e, blogs.ErrUnverified) {
		t.Fatal("removed domain authorized post", e)
	}
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
}
func TestPGBlogVerificationFencesDeletionAndLateResults(t *testing.T) {
	f := newBlogFixture(t)
	domain := verifiedBlogDomain(t, f)
	entered, release := make(chan struct{}), make(chan struct{})
	f.configBlog.Verifier = blogVerifyFunc(func(ctx context.Context, _, _ string) (bool, error) {
		close(entered)
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
	f = blogPool(t, f)
	done := make(chan error, 1)
	go func() { _, e := f.domains.Recheck(f.ctx, f.actor, f.pub.ID, domain.ID); done <- e }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("verification did not start")
	}
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	if e := f.domains.Remove(ctx, f.actor, f.pub.ID, domain.ID); e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	if e := <-done; !errors.Is(e, blogs.ErrNotFound) {
		t.Fatal("late proof revived deletion", e)
	}
	rows, e := f.domains.List(f.ctx, f.actor, f.pub.ID)
	if e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
}
func TestPGBlogScheduledPublicationRechecksPermissionAndDomainIncarnation(t *testing.T) {
	f := newBlogFixture(t)
	domain := verifiedBlogDomain(t, f)
	p := createBlog(t, f, posts.Patch{PublishedAt: func() *time.Time { v := scheduleFuture(t, f.db); return &v }()})
	scheduleDue(t, f.db, p)
	processSchedule(t, f.postMediaFixture)
	scheduleState(t, f.db, p.Schedule.ID, "published")
	changed := createBlog(t, f, posts.Patch{PublishedAt: func() *time.Time { v := scheduleFuture(t, f.db); return &v }()})
	if _, e := f.domains.Recheck(f.ctx, f.actor, f.pub.ID, domain.ID); e != nil {
		t.Fatal(e)
	}
	scheduleDue(t, f.db, changed)
	processSchedule(t, f.postMediaFixture)
	scheduleState(t, f.db, changed.Schedule.ID, "blocked")
	scheduleHidden(t, f.postMediaFixture, changed.ID)
	// A new but equivalent blog grant is a changed authorization incarnation.
	query(t, f.db, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1,'posts.create.blog',false)`, f.actor)
	replaced := createBlog(t, f, posts.Patch{PublishedAt: func() *time.Time { v := scheduleFuture(t, f.db); return &v }()})
	query(t, f.db, `DELETE FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create.blog'`, f.actor)
	query(t, f.db, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1,'posts.create.blog',false)`, f.actor)
	scheduleDue(t, f.db, replaced)
	processSchedule(t, f.postMediaFixture)
	scheduleState(t, f.db, replaced.Schedule.ID, "blocked")
	denied := createBlog(t, f, posts.Patch{PublishedAt: func() *time.Time { v := scheduleFuture(t, f.db); return &v }()})
	query(t, f.db, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1,'posts.create.blog',true)`, f.actor)
	scheduleDue(t, f.db, denied)
	processSchedule(t, f.postMediaFixture)
	scheduleState(t, f.db, denied.Schedule.ID, "blocked")
}
func TestPGBlogBackendAliasesAndStrictPermissionRequest(t *testing.T) {
	f := newBlogFixture(t)
	visibility := int32(0)
	app, e := application.NewPostgres(f.db, f.db, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &messagerpg.IndependentPolicy{NoneEncryptionMode: 0}, Blogs: &f.configBlog})
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/sphere/publishers/" + f.pub.Name + "/domains", "/api/publishers/" + f.pub.Name + "/domains"} {
		w := blogRequest(app, http.MethodGet, path, f.token, "")
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w := blogRequest(app, "POST", "/sphere/publishers/"+f.pub.Name+"/domains", f.token, `{"domain":"blog.example"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = blogRequest(app, "POST", "/sphere/posts/blog/check-permission?pub="+f.pub.Name, f.token, `{"url":"https://blog.example/article"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"can_create":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`null`, `{"url":"https://blog.example/a","url":"https://other.example"}`, `{"url":"https://blog.example/a","account_id":"ignored"}`} {
		w = blogRequest(app, "POST", "/api/posts/blog/check-permission", f.token, body)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = blogRequest(app, "POST", "/sphere/posts?pub="+f.pub.Name, f.token, `{"type":2,"content":"https://blog.example/article"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p posts.Post
	if e = json.Unmarshal(w.Body.Bytes(), &p); e != nil || p.Meta == nil {
		t.Fatal(e, w.Body.String())
	}
	w = blogRequest(app, "GET", "/api/posts/"+p.ID, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func blogSnapshot(t *testing.T, db *sql.DB, actor, pub string) string {
	t.Helper()
	var out string
	e := db.QueryRow(`SELECT jsonb_build_object('domains',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM owned_publisher_domains d WHERE d.publisher_id=$2),'posts',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM owned_text_posts p WHERE p.publisher_id=$2),'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM owned_publisher_domain_audit a WHERE a.actor_id=$1),'generation',(SELECT to_jsonb(g) FROM owned_publisher_blog_authority_epochs g WHERE g.account_id=$1),'intents',(SELECT jsonb_agg(to_jsonb(i) ORDER BY i.domain_id) FROM owned_publisher_domain_intents i WHERE i.actor_id=$1))::text`, actor, pub).Scan(&out)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestPGBlogSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type saved struct{ Actor, Publisher, Post, Domain, Snapshot string }
	if phase == "before" {
		f := newBlogFixture(t)
		d := verifiedBlogDomain(t, f)
		p := createBlog(t, f, posts.Patch{})
		saveRestartFixture(t, "blog-posts.json", saved{f.actor, f.pub.ID, p.ID, d.ID, blogSnapshot(t, db, f.actor, f.pub.ID)})
		return
	}
	var s saved
	loadRestartFixture(t, "blog-posts.json", &s)
	if actual := blogSnapshot(t, db, s.Actor, s.Publisher); actual != s.Snapshot {
		t.Fatal("blog/domain/audit state changed across restart")
	}
	r := postpg.New(db).WithVisibility(socialpg.New(db))
	p, e := r.Get(context.Background(), "", s.Post)
	if e != nil || p.Type != posts.Blog || p.Meta == nil || len(p.Meta.Embeds) != 1 || p.Meta.Embeds[0].Title != "Synthetic blog title" {
		t.Fatal(p, e)
	}
}

func blogRequest(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPGBlogProviderWaitsRecheckAuthorityAndManagerRole(t *testing.T) {
	f := newBlogFixture(t)
	query(t, f.db, `UPDATE owned_publisher_members SET role=50 WHERE publisher_id=$1 AND account_id=$2`, f.pub.ID, f.actor)
	if _, e := f.domains.List(f.ctx, f.actor, f.pub.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.domains.Add(f.ctx, f.actor, f.pub.ID, "blog.example"); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("editor changed domain", e)
	}
	if f.verifications.Load() != 0 {
		t.Fatal("unauthorized verification executed")
	}
	query(t, f.db, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1 AND account_id=$2`, f.pub.ID, f.actor)
	verifiedBlogDomain(t, f)
	f.configBlog.Previewer = blogPreviewFunc(func(ctx context.Context, u string) (reader.Link, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if e := authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}, {Pattern: "posts.create.blog", Deny: true}}, nil); e != nil {
			return reader.Link{}, e
		}
		return reader.Link{URL: u, Title: "Synthetic revoked preview"}, nil
	})
	f = blogPool(t, f)
	if _, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Type: postInt(posts.Blog), Content: postString("https://blog.example/article")}); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("in-flight permission revocation ignored", e)
	}
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
}

func TestPGBlogInterruptedVerificationRecoveryAndRevokedIntent(t *testing.T) {
	f := newBlogFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) { cancel(); return false, context.Canceled })
	f = blogPool(t, f)
	if _, e := f.domains.Add(ctx, f.actor, f.pub.Name, "blog.example"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	rows, e := f.domains.List(f.ctx, f.actor, f.pub.ID)
	if e != nil || len(rows) != 1 || rows[0].Status != blogs.Pending {
		t.Fatal(rows, e)
	}
	f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	f = blogPool(t, f)
	if n, e := f.domains.ProcessPending(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := f.domains.ProcessPending(context.Background(), 100); e != nil || n != 0 {
		t.Fatal("duplicate recovery", n, e)
	}
	check, e := f.posts.CheckBlogPermission(f.ctx, f.actor, f.pub.ID, "https://blog.example/article")
	if e != nil || !check.CanCreate {
		t.Fatal(check, e)
	}
	ctx, cancel = context.WithCancel(f.ctx)
	f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) { cancel(); return false, context.Canceled })
	f = blogPool(t, f)
	if _, e = f.domains.Recheck(ctx, f.actor, f.pub.ID, rows[0].ID); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}, {Pattern: "publishers.domains.manage", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) {
		t.Error("revoked pending intent called provider")
		return true, nil
	})
	f = blogPool(t, f)
	if n, e := f.domains.ProcessPending(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	rows, e = f.domains.List(f.ctx, f.actor, f.pub.ID)
	if e != nil || rows[0].Status != blogs.Revoked {
		t.Fatal(rows, e)
	}
}

func TestPGBlogRealmPublisherMaintainsMembershipPrivacy(t *testing.T) {
	f := newRealmPublishingFixture(t)
	query(t, f.db, blogpg.Schema)
	config := blogpg.Config{Now: f.config.Now, Verifier: blogVerifyFunc(func(context.Context, string, string) (bool, error) { return true, nil }), Previewer: blogPreviewFunc(func(_ context.Context, u string) (reader.Link, error) {
		return reader.Link{URL: u, Title: "Synthetic realm blog"}, nil
	})}
	domains, e := blogpg.New(f.db, f.guard, config)
	if e != nil {
		t.Fatal(e)
	}
	f.posts.WithBlogs(domains)
	if _, e = domains.Add(f.ctx, f.actor, f.privatePub.Name, "blog.example"); e != nil {
		t.Fatal(e)
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.privatePub.Name, posts.Patch{Type: postInt(posts.Blog), Content: postString("https://blog.example/private-realm")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Get(context.Background(), "", p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private realm blog exposed", e)
	}
	if _, e = f.posts.Get(f.outsideCtx, f.outside, p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("outsider read private realm blog", e)
	}
	if _, e = domains.List(f.outsideCtx, f.outside, f.privatePub.Name); e == nil {
		t.Fatal("outsider read private realm domain records")
	}
}

func TestPGBlogMetadataRollbackAndDraftPublication(t *testing.T) {
	f := newBlogFixture(t)
	domain := verifiedBlogDomain(t, f)
	if _, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("ordinary post"), BlogEmbeds: true}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("ordinary post silently consumed blog embeds", e)
	}
	now := time.Now().UTC()
	p := createBlog(t, f, posts.Patch{DraftedAt: &now})
	if _, e := f.posts.Get(context.Background(), "", p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal(e)
	}
	w := posts.NewHandler(f.posts, blogFixtureAuth{f.actor})
	// The caller's old link metadata is only a request envelope; fresh server data
	// replaces it. This mirrors the authored editor's embeds round trip.
	r := httptest.NewRequest("PATCH", "/api/posts/"+p.ID, strings.NewReader(`{"embeds":[{"type":"link","url":"https://attacker.example/","title":"Forged title"}],"title":""}`))
	r = r.WithContext(f.ctx)
	r.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	w.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var got posts.Post
	if e := json.Unmarshal(out.Body.Bytes(), &got); e != nil || got.Meta.Embeds[0].URL != "https://blog.example/article" || *got.Title != "Synthetic blog title" {
		t.Fatal(got, e)
	}
	file, _, e := f.repo.Upload(f.otherCtx, f.other, mediaUpload("blog-other-file", "private owner bytes", false), strings.NewReader("private owner bytes"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Content: postString("https://blog.example/never-committed"), Attachments: mediaIDs(file.ID)}); e == nil {
		t.Fatal("unauthorized attachment accepted")
	}
	current, e := f.posts.Get(f.ctx, f.actor, p.ID)
	if e != nil || current.Content != p.Content || current.Meta.Embeds[0].URL != p.Content {
		t.Fatal("failed association changed blog metadata", current, e)
	}
	if e = f.domains.Remove(f.ctx, f.actor, f.pub.ID, domain.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Publish(f.ctx, f.actor, p.ID); !errors.Is(e, blogs.ErrUnverified) {
		t.Fatal("unverified draft published", e)
	}
	scheduleHidden(t, f.postMediaFixture, p.ID)
}

type blogFixtureAuth struct{ actor string }

func (a blogFixtureAuth) Authenticate(*http.Request) (string, error) { return a.actor, nil }
func (a blogFixtureAuth) Authorize(*http.Request, string) error      { return nil }
