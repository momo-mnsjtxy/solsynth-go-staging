// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type postMediaFixture struct {
	mediaFixture
	posts *postpg.Repository
	links *postmediapg.Repository
	pub   accounts.Publisher
}

func newPostMediaFixture(t *testing.T) postMediaFixture {
	t.Helper()
	f := postMediaFixture{mediaFixture: newMediaFixture(t)}
	for _, id := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,4,now())`, id)
	}
	var e error
	f.pub, e = accountpg.NewForCompatibilityTests(f.db).CreatePublisher(context.Background(), f.actor, accounts.PublisherPatch{Name: postString("pm-" + f.actor), Nick: postString("Synthetic media publisher")})
	if e != nil {
		t.Fatal(e)
	}
	return postMediaPool(t, f, f.db)
}
func postMediaPool(t *testing.T, f postMediaFixture, db *sql.DB) postMediaFixture {
	t.Helper()
	f.mediaFixture = mediaPool(t, f.mediaFixture, db)
	visible := socialpg.New(db)
	f.links = postmediapg.New(db, interactionpg.New(db).WithVisibility(visible)).WithVisibility(visible)
	if e := f.links.Ready(context.Background()); e != nil {
		t.Fatal("post-media schema must be installed by guarded runner", e)
	}
	f.posts = postpg.NewWithAuthority(db, f.guard).WithVisibility(visible).WithAssociations(f.links)
	f.repo.WithReadVisibility(f.links)
	return f
}
func mediaIDs(ids ...string) *[]string { return &ids }
func attachPost(t *testing.T, f postMediaFixture, visibility int, ids ...string) posts.Post {
	t.Helper()
	p, e := f.posts.Create(f.ctx, f.actor, f.pub.Name, posts.Patch{Content: postString("post with file"), Visibility: &visibility, Attachments: mediaIDs(ids...)})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func privatePostFile(t *testing.T, f postMediaFixture, key, body string) media.File {
	t.Helper()
	file, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload(key, body, false), strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	return file
}
func missingPostFile(t *testing.T, f postMediaFixture, ctx context.Context, actor, id string) {
	t.Helper()
	file, body, e := f.repo.Open(ctx, actor, id)
	if body != nil {
		body.Close()
	}
	if !errors.Is(e, media.ErrNotFound) || file.ID != "" || body != nil {
		t.Fatal("file exposure", file, e)
	}
}
func postMediaHTTP(f postMediaFixture) http.Handler {
	mh := mediaHTTP(f.mediaFixture)
	ph := posts.NewVerifiedHandler(f.posts, f.verifier)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/posts") {
			if p, e := f.verifier.AuthenticateRequest(r); e == nil {
				r = r.WithContext(f.guard.Bind(r.Context(), p))
			}
			ph.ServeHTTP(w, r)
			return
		}
		mh.ServeHTTP(w, r)
	})
}

func TestPGPostMediaComposeUploadAttachDownload(t *testing.T) {
	f := newPostMediaFixture(t)
	h := postMediaHTTP(f)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, e := writer.CreateFormFile("file", "synthetic.txt")
	if e != nil {
		t.Fatal(e)
	}
	part.Write([]byte("compose media"))
	writer.Close()
	req := httptest.NewRequest("POST", "/api/files/upload/direct", &buf)
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var uploaded map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &uploaded); e != nil {
		t.Fatal(e)
	}
	id := uploaded["id"].(string)
	missingPostFile(t, f, context.Background(), "", id)
	w = backendCall(t, h, "POST", "/api/posts?pub="+f.pub.Name, f.token, fmt.Sprintf(`{"content":"","attachments":[%q],"tags":["media-synthetic"]}`, id), 200)
	p := backendDecode[posts.Post](t, w)
	if p.Content != "" || len(p.Attachments) != 1 || p.Attachments[0]["id"] != id || p.Attachments[0]["is_public"] != false || p.Attachments[0]["hash"] != nil || p.Attachments[0]["size"] != float64(13) || len(p.Tags) != 1 {
		t.Fatal(p)
	}
	for _, path := range []string{"/api/media/files/" + id, "/api/files/" + id + ".txt?original=true"} {
		w = backendCall(t, h, "GET", path, "", "", 200)
		if w.Body.String() != "compose media" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal(w.Header(), w.Body.String())
		}
	}
	req = httptest.NewRequest("GET", "/api/files/"+id, nil)
	req.Header.Set("Range", "bytes=0-6")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 206 || w.Body.String() != "compose" {
		t.Fatal(w.Code, w.Body.String())
	}
	req = httptest.NewRequest("HEAD", "/api/files/"+id, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "13" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	mediaUsage(t, f.mediaFixture, 13, 0)
	// Removing the only content-bearing attachment must roll back both changes.
	backendCall(t, h, "PATCH", "/api/posts/"+p.ID, f.token, `{"attachments":[]}`, 400)
	got, e := f.posts.Get(f.ctx, f.actor, p.ID)
	if e != nil || len(got.Attachments) != 1 {
		t.Fatal(got, e)
	}
	w = backendCall(t, h, "PATCH", "/api/posts/"+p.ID, f.token, `{"content":"text replaces attachment","attachments":[]}`, 200)
	detached := backendDecode[posts.Post](t, w)
	if len(detached.Attachments) != 0 {
		t.Fatal(detached)
	}
	missingPostFile(t, f, context.Background(), "", id)
	mediaRead(t, f.repo, f.ctx, f.actor, id, "compose media")
	mediaUsage(t, f.mediaFixture, 13, 0)
}

func TestPGPostMediaVisibilityAndRevocation(t *testing.T) {
	f := newPostMediaFixture(t)
	file := privatePostFile(t, f, "audience", "private upload")
	p := attachPost(t, f, posts.Private, file.ID)
	missingPostFile(t, f, context.Background(), "", file.ID)
	missingPostFile(t, f, f.otherCtx, f.other, file.ID)
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: postInt(posts.Public)}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, context.Background(), "", file.ID, "private upload")
	var public bool
	if e := f.db.QueryRow(`SELECT is_public FROM owned_media_files WHERE id=$1`, file.ID).Scan(&public); e != nil || public {
		t.Fatal("attachment promoted source privacy", public, e)
	}
	etag := backendCall(t, postMediaHTTP(f), "GET", "/api/files/"+file.ID, "", "", 200).Header().Get("ETag")
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: postInt(posts.Private)}); e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "/api/files/"+file.ID, nil)
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	postMediaHTTP(f).ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("conditional request bypassed revocation", w.Code)
	}
	s := socialpg.New(f.db)
	if _, e := s.ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.RequestFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ChangeRelationship(context.Background(), f.other, f.actor, social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: postInt(posts.Friends)}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, "private upload")
	missingPostFile(t, f, context.Background(), "", file.ID)
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: postInt(posts.CloseFriends)}); e != nil {
		t.Fatal(e)
	}
	missingPostFile(t, f, f.otherCtx, f.other, file.ID)
	if _, e := s.ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.SetClose}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, "private upload")
	if _, e := s.ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	missingPostFile(t, f, f.otherCtx, f.other, file.ID)
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: postInt(posts.Public)}); e != nil {
		t.Fatal(e)
	}
	missingPostFile(t, f, f.otherCtx, f.other, file.ID)
	mediaRead(t, f.repo, context.Background(), "", file.ID, "private upload")
	if e := f.posts.Delete(f.ctx, f.actor, p.ID); e != nil {
		t.Fatal(e)
	}
	missingPostFile(t, f, context.Background(), "", file.ID)
	// Independent public-media permission survives parent deletion.
	pub, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("explicit-public", "public", true), strings.NewReader("public"))
	if e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, context.Background(), "", pub.ID, "public")
	p = attachPost(t, f, posts.Public, file.ID)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	missingPostFile(t, f, context.Background(), "", file.ID)
	missingPostFile(t, f, context.Background(), "", pub.ID)
	if _, e = f.posts.Get(context.Background(), "", p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("disabled publisher projection", e)
	}
}

func TestPGPostMediaAtomicOwnershipAndReuse(t *testing.T) {
	f := newPostMediaFixture(t)
	a := privatePostFile(t, f, "owned-a", "A")
	b := privatePostFile(t, f, "owned-b", "BB")
	p := attachPost(t, f, posts.Public, b.ID, a.ID)
	if p.Attachments[0]["id"] != b.ID || p.Attachments[1]["id"] != a.ID {
		t.Fatal("composer order", p.Attachments)
	}
	otherPub, e := accountpg.NewForCompatibilityTests(f.db).CreatePublisher(context.Background(), f.other, accounts.PublisherPatch{Name: postString("other-" + f.other), Nick: postString("Other")})
	if e != nil {
		t.Fatal(e)
	}
	// Visibility through an existing public post is not private-file reuse consent.
	if _, e = f.posts.Create(f.otherCtx, f.other, otherPub.Name, posts.Patch{Content: postString("copy"), Attachments: mediaIDs(a.ID)}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("private cross-post reuse", e)
	}
	foreign, _, e := f.repo.Upload(f.otherCtx, f.other, mediaUpload("foreign", "foreign", false), strings.NewReader("foreign"))
	if e != nil {
		t.Fatal(e)
	}
	tags := []string{"must-rollback-" + f.actor}
	if _, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Content: postString("must roll back"), Attachments: mediaIDs(a.ID, foreign.ID), Tags: &tags}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal(e)
	}
	got, e := f.posts.Get(f.ctx, f.actor, p.ID)
	if e != nil || got.Content != "post with file" || len(got.Attachments) != 2 || got.Attachments[0]["id"] != b.ID || len(got.Tags) != 0 {
		t.Fatal("non-atomic attachment failure", got, e)
	}
	var tagged int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_taxonomy.post_tags WHERE slug=$1`, tags[0]).Scan(&tagged); e != nil || tagged != 0 {
		t.Fatal("taxonomy side effect", tagged, e)
	}
	joinPostEditor(t, f.db, f.actor, f.other, f.pub.ID)
	if _, e = f.posts.Update(f.otherCtx, f.other, p.ID, posts.Patch{Attachments: mediaIDs(a.ID, b.ID)}); e != nil {
		t.Fatal("same-post editor retention", e)
	}
	// Nonowners can attach explicitly public files, but such a link carries no
	// independent permission if that public flag is later revoked.
	pub, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("reusable", "shared", true), strings.NewReader("shared"))
	if e != nil {
		t.Fatal(e)
	}
	reused, e := f.posts.Create(f.otherCtx, f.other, otherPub.Name, posts.Patch{Content: postString("public reference"), Attachments: mediaIDs(pub.ID)})
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_media_files SET is_public=false WHERE id=$1`, pub.ID)
	missingPostFile(t, f, context.Background(), "", pub.ID)
	got, e = f.posts.Get(context.Background(), "", reused.ID)
	if e != nil || len(got.Attachments) != 0 {
		t.Fatal("revoked public source projected", got, e)
	}
	if _, e = f.repo.Delete(f.ctx, f.actor, b.ID); e != nil {
		t.Fatal(e)
	}
	got, e = f.posts.Get(context.Background(), "", p.ID)
	if e != nil || len(got.Attachments) != 1 || got.Attachments[0]["id"] != a.ID {
		t.Fatal("deleted source projected", got, e)
	}
	mediaUsage(t, f.mediaFixture, 7, 0) // A + shared; deleted BB released only its own charge.
	noAdapter := postpg.NewWithAuthority(f.db, f.guard).WithAssociations(interactionpg.New(f.db))
	if _, e = noAdapter.Create(f.ctx, f.actor, f.pub.Name, posts.Patch{Content: postString("missing adapter"), Attachments: mediaIDs(a.ID)}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("attachment ignored", e)
	}
}

func TestPGPostMediaAuthorityAndFileLockOrdering(t *testing.T) {
	f := newPostMediaFixture(t)
	file := privatePostFile(t, f, "ordering", "body")
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.posts.Create(ctx, f.actor, f.pub.Name, posts.Patch{Content: postString("revoked"), Attachments: mediaIDs(file.ID)})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("stale post permission", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	blocker, pid = atomicBlocker(t, locks, "owned_media_files", file.ID)
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting' WHERE id=$1`, file.ID); e != nil {
		t.Fatal(e)
	}
	go func() {
		_, e := f.posts.Create(ctx, f.actor, f.pub.Name, posts.Patch{Content: postString("deleted while waiting"), Attachments: mediaIDs(file.ID)})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("stale ready state", e)
	}
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_post_media WHERE file_id=$1`, file.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if _, e := f.repo.Cleanup(context.Background(), 10); e != nil {
		t.Fatal(e)
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
}

func TestPGPostMediaIndependentPoolRaceAndMissingBytes(t *testing.T) {
	f := newPostMediaFixture(t)
	file := privatePostFile(t, f, "concurrent", "file")
	p := attachPost(t, f, posts.Public, file.ID)
	fresh := postMediaPool(t, f, database(t))
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		_, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Attachments: mediaIDs(file.ID)})
		done <- e
	}()
	go func() { <-start; _, e := fresh.repo.Delete(fresh.ctx, fresh.actor, file.ID); done <- e }()
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil && !errors.Is(e, posts.ErrNotFound) {
			t.Fatal(e)
		}
	}
	missingPostFile(t, f, context.Background(), "", file.ID)
	got, e := fresh.posts.Get(context.Background(), "", p.ID)
	if e != nil || len(got.Attachments) != 0 {
		t.Fatal(got, e)
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
	file = privatePostFile(t, f, "missing-bytes", "missing")
	attachPost(t, f, posts.Public, file.ID)
	if e = os.Remove(filepath.Join(f.root, file.ID)); e != nil {
		t.Fatal(e)
	}
	if _, body, e := fresh.repo.Open(context.Background(), "", file.ID); !errors.Is(e, media.ErrUnavailable) || body != nil {
		t.Fatal("missing object hidden", e)
	}
}

func TestPGPostMediaRestartPersistence(t *testing.T) {
	db := database(t)
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	objects := filepath.Join(root, "post-media-objects")
	record := filepath.Join(root, "post-media-record.json")
	type saved struct{ PostID, FileID, Actor string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newPostMediaFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.root = objects
		f = postMediaPool(t, f, db)
		file := privatePostFile(t, f, "durable", "durable attachment")
		p := attachPost(t, f, posts.Public, file.ID)
		b, e := json.Marshal(saved{p.ID, file.ID, f.actor})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(record, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	var state saved
	if e = json.Unmarshal(b, &state); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	now := &atomic.Int64{}
	now.Store(time.Now().Unix())
	v, e := authority.NewVerifier(authoritypg.New(db), authorityConfig(t, authorityKeys(t), now))
	if e != nil {
		t.Fatal(e)
	}
	links := postmediapg.New(db, interactionpg.New(db))
	repo, e := mediapg.New(db, store, authoritypg.NewTransactionGuard(authoritypg.New(db), v), mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	repo.WithReadVisibility(links)
	mediaRead(t, repo, context.Background(), "", state.FileID, "durable attachment")
	p, e := postpg.New(db).WithAssociations(links).WithVisibility(socialpg.New(db)).Get(context.Background(), "", state.PostID)
	if e != nil || len(p.Attachments) != 1 || p.Attachments[0]["id"] != state.FileID || p.Attachments[0]["is_public"] != false {
		t.Fatal(p, e)
	}
	var used, reserved int64
	if e = db.QueryRow(`SELECT used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, state.Actor).Scan(&used, &reserved); e != nil || used != 18 || reserved != 0 {
		t.Fatal(used, reserved, e)
	}
}

// Keep the interface assertion close to integration coverage: composition must
// retain direct and resumable operations on the same canonical repository.
var _ media.DirectRepository = (*mediapg.Repository)(nil)
var _ media.ResumableRepository = (*mediapg.Repository)(nil)
