// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func usageDirectRequest(t *testing.T, h http.Handler, token, name string, before, after map[string]string, status int) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range before {
		if e := writer.WriteField(k, v); e != nil {
			t.Fatal(e)
		}
	}
	part, e := writer.CreateFormFile("file", name)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = part.Write([]byte("bytes")); e != nil {
		t.Fatal(e)
	}
	for k, v := range after {
		if e := writer.WriteField(k, v); e != nil {
			t.Fatal(e)
		}
	}
	if e = writer.Close(); e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", "/api/files/upload/direct", &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatal(name, w.Code, w.Body.String())
	}
	var out map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestPGMediaUsageDirectAtomicMetadata(t *testing.T) {
	f := newPostMediaFixture(t)
	h := postMediaHTTP(f)
	before := usageDirectRequest(t, h, f.token, "before.txt", map[string]string{"usage": "post"}, nil, 200)
	after := usageDirectRequest(t, h, f.token, "after.txt", nil, map[string]string{"usage": "post"}, 200)
	plain := usageDirectRequest(t, h, f.token, "plain.txt", nil, nil, 200)
	if plain["usage"] != nil {
		t.Fatal("no-usage default changed", plain)
	}
	for _, data := range []map[string]any{before, after} {
		id := data["id"].(string)
		if data["usage"] != "post" || data["is_public"] != false {
			t.Fatal(data)
		}
		got, e := f.repo.Info(f.ctx, f.actor, id)
		if e != nil || got.Usage != "post" {
			t.Fatal(got, e)
		}
		missingPostFile(t, f, context.Background(), "", id)
		p := attachPost(t, f, posts.Public, id)
		if len(p.Attachments) != 1 || p.Attachments[0]["usage"] != "post" || p.Attachments[0]["is_public"] != false {
			t.Fatal(p)
		}
		mediaRead(t, f.repo, context.Background(), "", id, "bytes")
	}
	mediaUsage(t, f.mediaFixture, 15, 0)
	for _, value := range []string{"chat", "POST", "public", "post\x00"} {
		usageDirectRequest(t, h, f.token, "bad-before.txt", map[string]string{"usage": value}, nil, 501)
		usageDirectRequest(t, h, f.token, "bad-after.txt", nil, map[string]string{"usage": value}, 501)
	}
	usageDirectRequest(t, h, f.token, "pool.txt", nil, map[string]string{"usage": "post", "pool_id": authorityUUID(t)}, 501)
	usageDirectRequest(t, h, f.token, "duplicate.txt", map[string]string{"usage": "post"}, map[string]string{"usage": "post"}, 400)
	mediaUsage(t, f.mediaFixture, 15, 0)
	files, e := os.ReadDir(f.root)
	if e != nil || len(files) != 3 {
		t.Fatal("rejected metadata leaked bytes", len(files), e)
	}
	// Metadata and ready publication share one SQL transaction after object write.
	query(t, f.db, `CREATE FUNCTION synthetic_media_usage_fail_ready() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='ready' AND NEW.name='fail.txt' AND NEW.usage='post' THEN RAISE EXCEPTION 'synthetic metadata publication failure'; END IF; RETURN NEW; END $$`)
	query(t, f.db, `CREATE TRIGGER synthetic_media_usage_fail_ready BEFORE UPDATE ON owned_media_files FOR EACH ROW EXECUTE FUNCTION synthetic_media_usage_fail_ready()`)
	defer query(t, f.db, `DROP TRIGGER synthetic_media_usage_fail_ready ON owned_media_files; DROP FUNCTION synthetic_media_usage_fail_ready()`)
	usageDirectRequest(t, h, f.token, "fail.txt", nil, map[string]string{"usage": "post"}, 503)
	mediaUsage(t, f.mediaFixture, 15, 0)
	files, e = os.ReadDir(f.root)
	if e != nil || len(files) != 3 {
		t.Fatal("publication failure leaked bytes", len(files), e)
	}
}

func TestPGMediaUsageResumableAndPostProjection(t *testing.T) {
	f := newPostMediaFixture(t)
	request := resumableRequest("hello", 3)
	request.Usage = "post"
	task, e := f.repo.CreateResumable(f.ctx, f.actor, request, "post-usage")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Create(f.ctx, f.actor, f.pub.Name, posts.Patch{Content: postString("pending"), Attachments: mediaIDs(task.TaskID)}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("pending source attached", e)
	}
	var usage string
	if e = f.db.QueryRow(`SELECT usage FROM owned_media_files WHERE id=$1`, task.TaskID).Scan(&usage); e != nil || usage != "post" {
		t.Fatal(usage, e)
	}
	replay, e := f.repo.CreateResumable(f.ctx, f.actor, request, "post-usage")
	if e != nil || replay.TaskID != task.TaskID {
		t.Fatal(replay, e)
	}
	changed := request
	changed.Usage = ""
	if _, e = f.repo.CreateResumable(f.ctx, f.actor, changed, "post-usage"); !errors.Is(e, media.ErrConflict) {
		t.Fatal("changed usage replayed", e)
	}
	for _, mutate := range []func(*media.CreateUploadRequest){func(r *media.CreateUploadRequest) { r.Usage = "chat" }, func(r *media.CreateUploadRequest) { r.PoolID = authorityUUID(t) }} {
		bad := request
		mutate(&bad)
		if _, e = f.repo.CreateResumable(f.ctx, f.actor, bad, "unsupported"); !errors.Is(e, media.ErrUnsupported) {
			t.Fatal(e)
		}
	}
	uploadResumableChunk(t, f.mediaFixture, task.TaskID, 0, "hel")
	uploadResumableChunk(t, f.mediaFixture, task.TaskID, 1, "lo")
	if e = f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal(e)
	}
	done := awaitResumable(t, f.mediaFixture, task.TaskID, "Completed")
	if done.FileID == nil || *done.FileID != task.TaskID {
		t.Fatal(done)
	}
	file, e := f.repo.Info(f.ctx, f.actor, task.TaskID)
	if e != nil || file.Usage != "post" || file.Public {
		t.Fatal(file, e)
	}
	missingPostFile(t, f, context.Background(), "", task.TaskID)
	draftTime := time.Now().UTC()
	p, e := f.posts.Create(f.ctx, f.actor, f.pub.Name, posts.Patch{Attachments: mediaIDs(task.TaskID), DraftedAt: &draftTime})
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Attachments) != 1 || p.Attachments[0]["usage"] != "post" {
		t.Fatal(p)
	}
	missingPostFile(t, f, context.Background(), "", task.TaskID)
	if _, e = f.posts.Publish(f.ctx, f.actor, p.ID); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, context.Background(), "", task.TaskID, "hello")
	mediaUsage(t, f.mediaFixture, 5, 0)
	// Legacy raw callers without usage still replay identically. A later request
	// that changes the metadata must not silently reuse that same idempotency key.
	raw := mediaUpload("raw-no-usage", "plain", false)
	file, _, e = f.repo.Upload(f.ctx, f.actor, raw, strings.NewReader("plain"))
	if e != nil || file.Usage != "" {
		t.Fatal(file, e)
	}
	raw.Usage = "post"
	if _, _, e = f.repo.Upload(f.ctx, f.actor, raw, strings.NewReader("plain")); !errors.Is(e, media.ErrConflict) {
		t.Fatal(e)
	}
	mediaUsage(t, f.mediaFixture, 10, 0)
}

func TestPGMediaUsageRestartPersistence(t *testing.T) {
	db := database(t)
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	objects := filepath.Join(root, "media-usage-objects")
	record := filepath.Join(root, "media-usage-record.json")
	type saved struct{ Direct, Resumable, Plain, Actor string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newMediaFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.root = objects
		f = mediaPool(t, f, db)
		direct := usageDirectRequest(t, mediaHTTP(f), f.token, "durable.txt", nil, map[string]string{"usage": "post"}, 200)
		request := resumableRequest("hello", 3)
		request.Usage = "post"
		task, e := f.repo.CreateResumable(f.ctx, f.actor, request, "restart-usage")
		if e != nil {
			t.Fatal(e)
		}
		uploadResumableChunk(t, f, task.TaskID, 0, "hel")
		uploadResumableChunk(t, f, task.TaskID, 1, "lo")
		if e = f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); e != nil {
			t.Fatal(e)
		}
		awaitResumable(t, f, task.TaskID, "Completed")
		plain, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("default-usage", "plain", false), strings.NewReader("plain"))
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(saved{direct["id"].(string), task.TaskID, plain.ID, f.actor})
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
	config := authorityConfig(t, authorityKeys(t), now)
	issuer, e := authority.New(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	verifier, e := authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	repo, e := mediapg.New(db, store, guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, issuer, state.Actor).Token
	request := httptest.NewRequest("GET", "/api/media/files", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	principal, e := verifier.AuthenticateRequest(request)
	if e != nil {
		t.Fatal(e)
	}
	ctx := guard.Bind(context.Background(), principal)
	for _, id := range []string{state.Direct, state.Resumable} {
		file, e := repo.Info(ctx, state.Actor, id)
		if e != nil || file.Usage != "post" || file.Public {
			t.Fatal(file, e)
		}
		if _, _, e = repo.Open(context.Background(), "", id); !errors.Is(e, media.ErrNotFound) {
			t.Fatal("usage granted access after restart", e)
		}
	}
	mediaRead(t, repo, ctx, state.Actor, state.Direct, "bytes")
	mediaRead(t, repo, ctx, state.Actor, state.Resumable, "hello")
	file, e := repo.Info(ctx, state.Actor, state.Plain)
	if e != nil || file.Usage != "" || media.LegacyFile(file)["usage"] != nil {
		t.Fatal(file, e)
	}
	quota, e := repo.Quota(ctx, state.Actor)
	if e != nil || quota.UsedBytes != 15 || quota.ReservedBytes != 0 {
		t.Fatal(quota, e)
	}
}

func TestPGMediaUsageAdditiveMigrationDefaults(t *testing.T) {
	db := database(t)
	tx, e := db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	// A transaction-local synthetic table models records from the previous schema;
	// pg_temp prevents the migration from targeting the canonical fixture table.
	if _, e = tx.Exec(`CREATE TEMP TABLE owned_media_files(id text PRIMARY KEY); INSERT INTO owned_media_files(id)VALUES('existing'); SET LOCAL search_path=pg_temp`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(mediapg.UsageSchema); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(mediapg.UsageSchema); e != nil {
		t.Fatal("upgrade replay", e)
	}
	var usage string
	if e = tx.QueryRow(`SELECT usage FROM owned_media_files WHERE id='existing'`).Scan(&usage); e != nil || usage != "" {
		t.Fatal("existing default", usage, e)
	}
	if _, e = tx.Exec(`INSERT INTO owned_media_files(id,usage)VALUES('new','post')`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`INSERT INTO owned_media_files(id,usage)VALUES('unknown','grant-everyone')`); e == nil {
		t.Fatal("unbounded usage accepted by storage")
	}
}
