// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

func resumableRequest(body string, chunk int64) media.CreateUploadRequest {
	h := md5.Sum([]byte(body))
	return media.CreateUploadRequest{Hash: hex.EncodeToString(h[:]), FileName: "resumable.bin", FileSize: int64(len(body)), ContentType: "application/octet-stream", ChunkSize: chunk}
}
func createResumable(t *testing.T, f mediaFixture, body, key string, chunk int64) media.UploadTask {
	t.Helper()
	task, e := f.repo.CreateResumable(f.ctx, f.actor, resumableRequest(body, chunk), key)
	if e != nil {
		t.Fatal(e)
	}
	return task
}
func uploadResumableChunk(t *testing.T, f mediaFixture, id string, index int, body string) {
	t.Helper()
	if _, e := f.repo.UploadChunk(f.ctx, f.actor, id, index, strings.NewReader(body)); e != nil {
		t.Fatal(e)
	}
}
func awaitResumable(t *testing.T, f mediaFixture, id, status string) media.UploadTask {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		task, e := f.repo.ResumableProgress(f.ctx, f.actor, id)
		if e != nil {
			t.Fatal(e)
		}
		if task.Status == status {
			return task
		}
		if time.Now().After(deadline) || task.Status == "Failed" && status != "Failed" {
			t.Fatalf("task did not reach %s: %+v", status, task)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPGMediaResumableLifecycleAndIsolation(t *testing.T) {
	f := newMediaFixture(t)
	task := createResumable(t, f, "hello", "lifecycle", 3)
	mediaUsage(t, f, 0, 5)
	if _, e := f.repo.Info(f.ctx, f.actor, task.TaskID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("pending file visible", e)
	}
	if e := f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); !errors.Is(e, media.ErrPending) {
		t.Fatal("incomplete accepted", e)
	}
	for _, operation := range []func() error{
		func() error { _, e := f.repo.ResumableProgress(f.otherCtx, f.other, task.TaskID); return e },
		func() error {
			_, e := f.repo.UploadChunk(f.otherCtx, f.other, task.TaskID, 0, strings.NewReader("hel"))
			return e
		},
		func() error { return f.repo.CompleteResumable(f.otherCtx, f.other, task.TaskID) },
		func() error { return f.repo.CancelResumable(f.otherCtx, f.other, task.TaskID) },
	} {
		if e := operation(); !errors.Is(e, media.ErrNotFound) {
			t.Fatal("cross-user task access", e)
		}
	}
	uploadResumableChunk(t, f, task.TaskID, 1, "lo")
	progress, e := f.repo.ResumableProgress(f.ctx, f.actor, task.TaskID)
	if e != nil || progress.ChunksUploaded != 1 || progress.UploadedChunks[0] != 1 || progress.Progress != 25 {
		t.Fatal(progress, e)
	}
	uploadResumableChunk(t, f, task.TaskID, 0, "hel")
	if replay, e := f.repo.UploadChunk(f.ctx, f.actor, task.TaskID, 0, strings.NewReader("hel")); e != nil || !replay {
		t.Fatal(replay, e)
	}
	if _, e := f.repo.UploadChunk(f.ctx, f.actor, task.TaskID, 0, strings.NewReader("bad")); !errors.Is(e, media.ErrConflict) {
		t.Fatal("changed replay", e)
	}
	if e := f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal(e)
	}
	complete := awaitResumable(t, f, task.TaskID, "Completed")
	if complete.Progress != 100 || complete.FileID == nil || *complete.FileID != task.TaskID {
		t.Fatal(complete)
	}
	if e := f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal("completion replay", e)
	}
	if replayed, e := f.repo.UploadChunk(f.ctx, f.actor, task.TaskID, 0, strings.NewReader("hel")); e != nil || !replayed {
		t.Fatal("completed chunk replay", replayed, e)
	}
	if _, e := f.repo.UploadChunk(f.ctx, f.actor, task.TaskID, 0, strings.NewReader("bad")); !errors.Is(e, media.ErrConflict) {
		t.Fatal("changed completed chunk replay", e)
	}
	mediaRead(t, f.repo, f.ctx, f.actor, task.TaskID, "hello")
	mediaUsage(t, f, 5, 0)
	if _, _, e := f.repo.Open(context.Background(), "", task.TaskID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("private upload exposed", e)
	}
	if e := f.repo.CancelResumable(f.ctx, f.actor, task.TaskID); !errors.Is(e, media.ErrConflict) {
		t.Fatal("cancel removed completed file", e)
	}
	files, e := os.ReadDir(f.root)
	if e != nil || len(files) != 1 || files[0].Name() != task.TaskID {
		t.Fatal("staging remains", files, e)
	}
	if _, e := f.repo.Delete(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal(e)
	}
	mediaUsage(t, f, 0, 0)
}

func TestPGMediaResumableQuotaAndIdempotencyConcurrency(t *testing.T) {
	f := newMediaFixture(t)
	request := resumableRequest("hello", 3)
	var wg sync.WaitGroup
	results := make(chan media.UploadTask, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, e := f.repo.CreateResumable(f.ctx, f.actor, request, "same-key")
			results <- task
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for task := range results {
		if id != "" && id != task.TaskID {
			t.Fatal("duplicate task", id, task.TaskID)
		}
		id = task.TaskID
	}
	mediaUsage(t, f, 0, 5)
	changed := request
	changed.ChunkSize = 4
	if _, e := f.repo.CreateResumable(f.ctx, f.actor, changed, "same-key"); !errors.Is(e, media.ErrConflict) {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=8 WHERE account_id=$1`, f.actor)
	if _, e := f.repo.CreateResumable(f.ctx, f.actor, request, "another"); !errors.Is(e, media.ErrQuota) {
		t.Fatal("quota bypass", e)
	}
	uploadResumableChunk(t, f, id, 0, "hel")
	uploadResumableChunk(t, f, id, 1, "lo")
	errs = make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() { errs <- f.repo.CompleteResumable(f.ctx, f.actor, id) }()
	}
	for i := 0; i < 6; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	awaitResumable(t, f, id, "Completed")
	mediaUsage(t, f, 5, 0)
	replay, e := f.repo.CreateResumable(f.ctx, f.actor, request, "same-key")
	if e != nil || replay.TaskID != id || replay.Status != "Completed" {
		t.Fatal(replay, e)
	}
	other, e := f.repo.ListResumable(f.otherCtx, f.other, media.UploadTaskQuery{SortBy: "lastActivity", Descending: true, Limit: 50})
	if e != nil || len(other) != 0 {
		t.Fatal(other, e)
	}
}

func TestPGMediaResumableBoundsHashFailureAndCleanup(t *testing.T) {
	f := newMediaFixture(t)
	task := createResumable(t, f, "hello", "invalid-chunks", 3)
	for _, tc := range []struct {
		index int
		body  string
	}{{-1, "hel"}, {2, "x"}, {0, "he"}, {0, "hell"}, {1, "loo"}} {
		if _, e := f.repo.UploadChunk(f.ctx, f.actor, task.TaskID, tc.index, strings.NewReader(tc.body)); e == nil {
			t.Fatal("bad chunk accepted", tc)
		}
	}
	progress, e := f.repo.ResumableProgress(f.ctx, f.actor, task.TaskID)
	if e != nil || progress.ChunksUploaded != 0 {
		t.Fatal(progress, e)
	}
	uploadResumableChunk(t, f, task.TaskID, 0, "bad")
	uploadResumableChunk(t, f, task.TaskID, 1, "!!")
	if e = f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal(e)
	}
	failed := awaitResumable(t, f, task.TaskID, "Failed")
	if failed.FailureCode != "MEDIA_CONTENT_MISMATCH" {
		t.Fatal(failed)
	}
	// Failed is committed before byte removal; the replayable cleanup joins the
	// same canonical lock and establishes quota release before asserting it.
	if _, e = f.repo.Cleanup(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	mediaUsage(t, f, 0, 0)
	entries, e := os.ReadDir(f.root)
	if e != nil || len(entries) != 0 {
		t.Fatal("failed upload retained bytes", entries, e)
	}
	if _, _, e = f.repo.Open(context.Background(), "", task.TaskID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("hash failure published", e)
	}
	if e = f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); !errors.Is(e, media.ErrGone) {
		t.Fatal("failed task resurrected", e)
	}
}

func TestPGMediaResumablePublicationRecovery(t *testing.T) {
	f := newMediaFixture(t)
	task := createResumable(t, f, "hello", "recovery", 3)
	uploadResumableChunk(t, f, task.TaskID, 0, "hel")
	uploadResumableChunk(t, f, task.TaskID, 1, "lo")
	query(t, f.db, `CREATE FUNCTION synthetic_resumable_fail_ready() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic resumable metadata failure'; END $$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_resumable_fail_ready BEFORE UPDATE ON owned_media_files FOR EACH ROW WHEN (NEW.state='ready' AND NEW.id='%s'::uuid) EXECUTE FUNCTION synthetic_resumable_fail_ready()`, task.TaskID))
	defer query(t, f.db, `DROP FUNCTION IF EXISTS synthetic_resumable_fail_ready() CASCADE`)
	if e := f.repo.CompleteResumable(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(f.root, task.TaskID)); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("assembly did not publish object")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Acquiring the owner lock establishes that the first worker's SQL failure
	// rolled back; the durable final object must remain hidden and reserved.
	mediaUsage(t, f, 0, 5)
	if _, e := f.repo.Info(f.ctx, f.actor, task.TaskID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("uncommitted metadata visible", e)
	}
	query(t, f.db, `DROP TRIGGER synthetic_resumable_fail_ready ON owned_media_files`)
	fresh := mediaPool(t, f, database(t))
	awaitResumable(t, fresh, task.TaskID, "Completed")
	mediaRead(t, fresh.repo, fresh.ctx, fresh.actor, task.TaskID, "hello")
	mediaUsage(t, fresh, 5, 0)
	if _, e := os.Stat(filepath.Join(f.root, task.TaskID+".chunks")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("staging remains", e)
	}
}

func TestPGMediaResumableExpiryCancellationAndFreshAuthority(t *testing.T) {
	f := newMediaFixture(t)
	task := createResumable(t, f, "hello", "expiry", 3)
	uploadResumableChunk(t, f, task.TaskID, 0, "hel")
	query(t, f.db, `UPDATE owned_media_files SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, task.TaskID)
	if _, e := f.repo.UploadChunk(f.ctx, f.actor, task.TaskID, 1, strings.NewReader("lo")); !errors.Is(e, media.ErrGone) {
		t.Fatal("expired revived", e)
	}
	if _, e := f.repo.Cleanup(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	mediaUsage(t, f, 0, 0)
	progress, e := f.repo.ResumableProgress(f.ctx, f.actor, task.TaskID)
	if e != nil || progress.Status != "Expired" {
		t.Fatal(progress, e)
	}
	task = createResumable(t, f, "hello", "cancel", 3)
	uploadResumableChunk(t, f, task.TaskID, 0, "hel")
	if e = f.repo.CancelResumable(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.CancelResumable(f.ctx, f.actor, task.TaskID); e != nil {
		t.Fatal("cancel replay", e)
	}
	mediaUsage(t, f, 0, 0)
	if _, e = f.repo.CreateResumable(f.ctx, f.actor, resumableRequest("hello", 3), "cancel"); !errors.Is(e, media.ErrGone) {
		t.Fatal("canceled resurrected", e)
	}
	task = createResumable(t, f, "hello", "fresh-policy", 3)
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: media.ReadPermission}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.UploadChunk(f.ctx, f.actor, task.TaskID, 0, strings.NewReader("hel")); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("stale policy accepted", e)
	}
	if _, e = f.repo.CreateResumable(context.Background(), f.actor, resumableRequest("hello", 3), "no-proof"); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("actor became proof", e)
	}
}

func TestPGMediaResumableHTTPMultipartAndAsyncContract(t *testing.T) {
	f := newMediaFixture(t)
	h := mediaHTTP(f)
	request, _ := json.Marshal(resumableRequest("hello", 3))
	r := httptest.NewRequest("POST", "/api/files/upload/create", bytes.NewReader(request))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		TaskID string `json:"task_id"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		index  int
		body   string
		extra  bool
		status int
	}{{0, "hel", true, 400}, {0, "hel", false, 200}, {1, "lo", false, 200}} {
		var b bytes.Buffer
		writer := multipart.NewWriter(&b)
		part, e := writer.CreateFormFile("chunk", "chunk.bin")
		if e != nil {
			t.Fatal(e)
		}
		part.Write([]byte(tc.body))
		if tc.extra {
			writer.WriteField("unexpected", "value")
		}
		writer.Close()
		r = httptest.NewRequest("POST", fmt.Sprintf("/api/files/upload/chunk/%s/%d", result.TaskID, tc.index), &b)
		r.Header.Set("Authorization", "Bearer "+f.token)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r = httptest.NewRequest("POST", "/api/files/upload/complete/"+result.TaskID, nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"taskId":"`+result.TaskID+`"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitResumable(t, f, result.TaskID, "Completed")
	r = httptest.NewRequest("GET", "/api/files/"+result.TaskID, nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Range", "bytes=1-3")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "ell" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPGMediaResumableRestartPersistence(t *testing.T) {
	db := database(t)
	phase := os.Getenv("PG_TEST_PHASE")
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	objects := filepath.Join(root, "media-resumable-objects")
	record := filepath.Join(root, "media-resumable.json")
	type descriptor struct{ Actor, Other, Partial, Processing string }
	if phase == "before" {
		f := newMediaFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.root = objects
		f = mediaPool(t, f, db)
		partial := createResumable(t, f, "hello", "restart-partial", 3)
		uploadResumableChunk(t, f, partial.TaskID, 0, "hel")
		processing := createResumable(t, f, "world", "restart-processing", 3)
		uploadResumableChunk(t, f, processing.TaskID, 0, "wor")
		uploadResumableChunk(t, f, processing.TaskID, 1, "ld")
		// Durable accepted state with no in-memory worker models process loss.
		query(t, db, `UPDATE owned_media_upload_tasks SET status='Processing' WHERE id=$1`, processing.TaskID)
		query(t, db, `UPDATE owned_media_files SET expires_at=clock_timestamp()+interval '1 hour' WHERE account_id=$1 AND state='pending'`, f.actor)
		b, e := json.Marshal(descriptor{f.actor, f.other, partial.TaskID, processing.TaskID})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(record, b, 0600); e != nil {
			t.Fatal(e)
		}
		mediaUsage(t, f, 0, 10)
		return
	}
	if phase != "after" {
		t.Fatal("restart test requires explicit before/after phase")
	}
	b, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	var d descriptor
	if e = json.Unmarshal(b, &d); e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	issuer, e := authority.New(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	f := mediaFixture{db: db, actor: d.Actor, other: d.Other, root: objects, config: cfg, issuer: issuer, token: pgIssue(t, issuer, d.Actor).Token, otherToken: pgIssue(t, issuer, d.Other).Token}
	f = mediaPool(t, f, db)
	progress, e := f.repo.ResumableProgress(f.ctx, f.actor, d.Partial)
	if e != nil || progress.ChunksUploaded != 1 || progress.UploadedChunks[0] != 0 {
		t.Fatal(progress, e)
	}
	uploadResumableChunk(t, f, d.Partial, 1, "lo")
	if e = f.repo.CompleteResumable(f.ctx, f.actor, d.Partial); e != nil {
		t.Fatal(e)
	}
	awaitResumable(t, f, d.Partial, "Completed")
	awaitResumable(t, f, d.Processing, "Completed")
	mediaRead(t, f.repo, f.ctx, f.actor, d.Partial, "hello")
	mediaRead(t, f.repo, f.ctx, f.actor, d.Processing, "world")
	mediaUsage(t, f, 10, 0)
}
