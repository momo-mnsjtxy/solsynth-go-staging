// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

type mediaFixture struct {
	db                                    *sql.DB
	repo                                  *mediapg.Repository
	store                                 *mediastore.Filesystem
	guard                                 *authoritypg.TransactionGuard
	verifier                              *authority.Verifier
	config                                authority.Config
	issuer                                *authority.Service
	actor, other, token, otherToken, root string
	ctx, otherCtx                         context.Context
}

func mediaDigest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func mediaUpload(key, body string, public bool) media.Upload {
	return media.Upload{Name: "sample.txt", MIMEType: "text/plain", Size: int64(len(body)), SHA256: mediaDigest(body), IdempotencyKey: key, Public: public}
}
func newMediaFixture(t *testing.T) mediaFixture {
	t.Helper()
	return newMediaFixtureInDatabase(t, database(t))
}

// newMediaFixtureInDatabase keeps all seeds and service pools in the caller's
// database. Restart fixtures may isolate global maintenance interference.
func newMediaFixtureInDatabase(t *testing.T, db *sql.DB) mediaFixture {
	t.Helper()
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	f := mediaFixture{db: db, config: cfg, actor: authorityUUID(t), other: authorityUUID(t), root: t.TempDir()}
	if err := os.Chmod(f.root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.actor, f.other} {
		if err := authoritypg.SeedSynthetic(context.Background(), db, id, "synthetic_media_"+strings.ReplaceAll(id, "-", ""), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := authoritypg.New(db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "media.files.*"}}, nil); err != nil {
			t.Fatal(err)
		}
		query(t, db, `INSERT INTO owned_media_quotas(account_id,limit_bytes) VALUES($1,65536)`, id)
	}
	var err error
	f.issuer, err = authority.New(authoritypg.New(db), cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	return mediaPool(t, f, db)
}
func mediaPool(t *testing.T, f mediaFixture, db *sql.DB) mediaFixture {
	t.Helper()
	f.db = db
	var err error
	f.verifier, err = authority.NewVerifier(authoritypg.New(db), f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(db), f.verifier)
	f.store, err = mediastore.NewFilesystem(f.root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.store.Close() })
	f.repo, err = mediapg.New(db, f.store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.ctx = mediaProof(t, f, f.token)
	f.otherCtx = mediaProof(t, f, f.otherToken)
	if err = f.repo.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}
func mediaProof(t *testing.T, f mediaFixture, token string) context.Context {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/media/files", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	p, e := f.verifier.AuthenticateRequest(r)
	if e != nil {
		t.Fatal(e)
	}
	return f.guard.Bind(context.Background(), p)
}
func mediaRead(t *testing.T, r *mediapg.Repository, ctx context.Context, actor, id, want string) {
	t.Helper()
	_, body, e := r.Open(ctx, actor, id)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(body)
	body.Close()
	if e != nil || string(b) != want {
		t.Fatal("bytes", string(b), e)
	}
}
func mediaUsage(t *testing.T, f mediaFixture, used, reserved int64) {
	t.Helper()
	q, e := f.repo.Quota(f.ctx, f.actor)
	if e != nil || q.UsedBytes != used || q.ReservedBytes != reserved {
		t.Fatal("quota", q, e)
	}
}

func TestPGMediaLifecycleAndIsolation(t *testing.T) {
	f := newMediaFixture(t)
	u := mediaUpload("one", "hello", false)
	file, replayed, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("hello"))
	if e != nil || replayed || file.State != "ready" {
		t.Fatal(file, replayed, e)
	}
	mediaRead(t, f.repo, f.ctx, f.actor, file.ID, "hello")
	for _, actor := range []string{"", f.other} {
		ctx := context.Background()
		if actor != "" {
			ctx = f.otherCtx
		}
		if _, _, e = f.repo.Open(ctx, actor, file.ID); !errors.Is(e, media.ErrNotFound) {
			t.Fatal("private visibility", actor, e)
		}
	}
	same, replayed, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("hello"))
	if e != nil || !replayed || same.ID != file.ID {
		t.Fatal("idempotent upload", same, e)
	}
	changed := u
	changed.Public = true
	if _, _, e = f.repo.Upload(f.ctx, f.actor, changed, strings.NewReader("hello")); !errors.Is(e, media.ErrConflict) {
		t.Fatal(e)
	}
	if _, e = f.repo.Delete(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("other deleted", e)
	}
	pub, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("public", "world", true), strings.NewReader("world"))
	if e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, context.Background(), "", pub.ID, "world")
	mediaUsage(t, f, 10, 0)
	if _, e = f.repo.Delete(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Delete(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal("delete replay", e)
	}
	if _, _, e = f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("hello")); !errors.Is(e, media.ErrGone) {
		t.Fatal("resurrected upload", e)
	}
	mediaUsage(t, f, 5, 0)
	if _, e = os.Stat(filepath.Join(f.root, file.ID)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("object remains", e)
	}
	if _, _, e = f.repo.Upload(context.Background(), f.actor, mediaUpload("forged", "hello", false), strings.NewReader("hello")); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("account string became proof", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	if _, _, e = f.repo.Open(context.Background(), "", pub.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("disabled owner exposed", e)
	}
}

func TestPGMediaQuotaAndIdempotencyConcurrency(t *testing.T) {
	f := newMediaFixture(t)
	other := mediaPool(t, f, database(t))
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=10 WHERE account_id=$1`, f.actor)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, x := range []mediaFixture{f, other} {
		go func(i int, x mediaFixture) {
			<-start
			_, _, e := x.repo.Upload(x.ctx, x.actor, mediaUpload(string(rune('a'+i)), "123456", false), strings.NewReader("123456"))
			results <- e
		}(i, x)
	}
	close(start)
	success, quota := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if errors.Is(e, media.ErrQuota) {
			quota++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || quota != 1 {
		t.Fatal(success, quota)
	}
	mediaUsage(t, f, 6, 0)
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=100 WHERE account_id=$1`, f.actor)
	start = make(chan struct{})
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	for _, x := range []mediaFixture{f, other} {
		wg.Add(1)
		go func(x mediaFixture) {
			defer wg.Done()
			<-start
			file, _, e := x.repo.Upload(x.ctx, x.actor, mediaUpload("same", "123456", false), strings.NewReader("123456"))
			if e != nil && !errors.Is(e, media.ErrPending) {
				results <- e
			} else {
				results <- nil
			}
			if e == nil {
				ids <- file.ID
			}
		}(x)
	}
	close(start)
	wg.Wait()
	close(ids)
	for i := 0; i < 2; i++ {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	last := ""
	for id := range ids {
		if last != "" && last != id {
			t.Fatal("duplicate objects", last, id)
		}
		last = id
	}
	mediaUsage(t, f, 12, 0)
}

type mediaFailingStore struct {
	media.ObjectStore
	failDelete bool
}

func (s *mediaFailingStore) Remove(ctx context.Context, id string) error {
	if s.failDelete {
		return errors.New("synthetic deletion unavailable")
	}
	return s.ObjectStore.Remove(ctx, id)
}

func TestPGMediaRollbackCleanupAndRecovery(t *testing.T) {
	f := newMediaFixture(t)
	bad := mediaUpload("bad", "hello", false)
	bad.SHA256 = mediaDigest("other")
	if _, _, e := f.repo.Upload(f.ctx, f.actor, bad, strings.NewReader("hello")); !errors.Is(e, media.ErrDigest) {
		t.Fatal(e)
	}
	mediaUsage(t, f, 0, 0)
	entries, e := os.ReadDir(f.root)
	if e != nil || len(entries) != 0 {
		t.Fatal("failed upload leak", entries, e)
	}
	store := &mediaFailingStore{ObjectStore: f.store, failDelete: true}
	repo, e := mediapg.New(f.db, store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	file, _, e := repo.Upload(f.ctx, f.actor, mediaUpload("delete", "hello", true), strings.NewReader("hello"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Delete(f.ctx, f.actor, file.ID); e == nil {
		t.Fatal("cleanup failure hidden")
	}
	mediaUsage(t, f, 5, 0)
	if _, _, e = repo.Open(context.Background(), "", file.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("deleting bytes exposed", e)
	}
	if n, e := f.repo.Cleanup(context.Background(), 10); e != nil || n != 1 {
		t.Fatal("retry", n, e)
	}
	mediaUsage(t, f, 0, 0)
	// A committed reservation and partial bytes model process loss mid-stream.
	id := authorityUUID(t)
	query(t, f.db, `UPDATE owned_media_quotas SET reserved_bytes=5 WHERE account_id=$1`, f.actor)
	query(t, f.db, `INSERT INTO owned_media_files(id,account_id,idempotency_key,name,mime_type,size,sha256,is_public,state,charge,expires_at,created_at,updated_at) VALUES($1,$2,'abandoned','sample.txt','text/plain',5,$3,false,'pending','reserved',now()-interval '1 hour',now(),now())`, id, f.actor, mediaDigest("hello"))
	if e = os.WriteFile(filepath.Join(f.root, id+".part"), []byte("hel"), 0600); e != nil {
		t.Fatal(e)
	}
	fresh := mediaPool(t, f, database(t))
	type cleaned struct {
		n   int
		err error
	}
	results := make(chan cleaned, 2)
	start := make(chan struct{})
	for _, repo := range []*mediapg.Repository{f.repo, fresh.repo} {
		go func(repo *mediapg.Repository) {
			<-start
			n, e := repo.Cleanup(context.Background(), 10)
			results <- cleaned{n, e}
		}(repo)
	}
	close(start)
	total := 0
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		total += result.n
	}
	if total != 1 {
		t.Fatal("cleanup transition replay", total)
	}
	mediaUsage(t, f, 0, 0)
	entries, e = os.ReadDir(f.root)
	if e != nil || len(entries) != 0 {
		t.Fatal("cleanup bytes", entries, e)
	}
}

func mediaHTTP(f mediaFixture) http.Handler {
	service := (&media.Service{Repository: f.repo, Authority: f.verifier, MaxFileBytes: 1 << 20}).Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			if p, e := f.verifier.AuthenticateRequest(r); e == nil {
				r = r.WithContext(f.guard.Bind(r.Context(), p))
			}
		}
		service.ServeHTTP(w, r)
	})
}
func TestPGMediaHTTPDirectAndRange(t *testing.T) {
	f := newMediaFixture(t)
	h := mediaHTTP(f)
	raw := httptest.NewRequest("POST", "/api/media/files", strings.NewReader("hello"))
	raw.Header.Set("Authorization", "Bearer "+f.token)
	raw.Header.Set("Content-Type", "text/plain")
	raw.Header.Set("X-File-Name", "sample.txt")
	raw.Header.Set("X-Content-SHA256", mediaDigest("hello"))
	raw.Header.Set("Idempotency-Key", "http")
	raw.Header.Set("X-File-Public", "true")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, raw)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	file := backendDecode[media.File](t, w)
	get := httptest.NewRequest("GET", "/api/files/"+file.ID+".txt?original=true", nil)
	get.Header.Set("Range", "bytes=1-3")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, get)
	if w.Code != 206 || w.Body.String() != "ell" || w.Header().Get("Content-Range") != "bytes 1-3/5" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	get = httptest.NewRequest("GET", "/api/files/"+file.ID, nil)
	get.Header.Set("Authorization", "Bearer invalid")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, get)
	if w.Code != 401 {
		t.Fatal("invalid credentials downgraded", w.Code)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, e := writer.CreateFormFile("file", "direct.txt")
	if e != nil {
		t.Fatal(e)
	}
	part.Write([]byte("direct"))
	writer.Close()
	direct := httptest.NewRequest("POST", "/api/files/upload/direct", &body)
	direct.Header.Set("Authorization", "Bearer "+f.token)
	direct.Header.Set("Content-Type", writer.FormDataContentType())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, direct)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var legacy map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &legacy); e != nil {
		t.Fatal(e)
	}
	if legacy["size"] != float64(6) || legacy["hash"] != nil || legacy["uploaded_at"] != nil || legacy["has_thumbnail"] != false {
		t.Fatal(legacy)
	}
	directID := legacy["id"].(string)
	mediaRead(t, f.repo, f.ctx, f.actor, directID, "direct")
	if _, _, e = f.repo.Open(context.Background(), "", directID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("direct default privacy", e)
	}
	mediaUsage(t, f, 11, 0)
	// Unsupported metadata after the file must roll the whole stream back.
	body.Reset()
	writer = multipart.NewWriter(&body)
	part, _ = writer.CreateFormFile("file", "reject.txt")
	part.Write([]byte("reject"))
	writer.WriteField("pool_id", authorityUUID(t))
	writer.Close()
	direct = httptest.NewRequest("POST", "/api/files/upload/direct", &body)
	direct.Header.Set("Authorization", "Bearer "+f.token)
	direct.Header.Set("Content-Type", writer.FormDataContentType())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, direct)
	if w.Code != 501 {
		t.Fatal("unsupported trailing option accepted", w.Code, w.Body.String())
	}
	mediaUsage(t, f, 11, 0)
}

func TestPGMediaFreshAuthorityAndCancellation(t *testing.T) {
	f := newMediaFixture(t)
	if err := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("revoked", "hello", false), strings.NewReader("hello")); e == nil {
		t.Fatal("revoked permission ignored")
	}
	if err := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "media.files.*"}}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, _, e := f.repo.Upload(ctx, f.actor, mediaUpload("canceled", "hello", false), strings.NewReader("hello")); e == nil {
		t.Fatal("canceled upload accepted")
	}
	mediaUsage(t, f, 0, 0)
	if err := f.issuer.InvalidateAccount(context.Background(), f.actor); err != nil {
		t.Fatal(err)
	}
	if _, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("stale", "hello", false), strings.NewReader("hello")); e == nil {
		t.Fatal("invalidated proof accepted")
	}
}

func TestPGMediaAuthoritySerialization(t *testing.T) {
	f := newMediaFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, _, e := f.repo.Upload(ctx, f.actor, mediaUpload("waiter", "hello", false), strings.NewReader("hello"))
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("preflight proof survived committed policy change", e)
	}
	var files, used, reserved int64
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_media_files WHERE account_id=$1`, f.actor).Scan(&files); e != nil {
		t.Fatal(e)
	}
	if e := f.db.QueryRow(`SELECT used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor).Scan(&used, &reserved); e != nil {
		t.Fatal(e)
	}
	if files != 0 || used != 0 || reserved != 0 {
		t.Fatal("unauthorized transaction side effects", files, used, reserved)
	}
}

func TestPGMediaMetadataFailureAfterObjectWrite(t *testing.T) {
	f := newMediaFixture(t)
	// The disposable schema trigger fails only this fixture's ready transition.
	// Real object bytes have been fsynced before the SQL failure is injected.
	query(t, f.db, `CREATE OR REPLACE FUNCTION synthetic_media_fail_ready() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='ready' AND NEW.idempotency_key='synthetic-fail-ready' THEN RAISE EXCEPTION 'synthetic media commit failure'; END IF; RETURN NEW; END $$`)
	query(t, f.db, `CREATE TRIGGER synthetic_media_fail_ready BEFORE UPDATE ON owned_media_files FOR EACH ROW EXECUTE FUNCTION synthetic_media_fail_ready()`)
	defer query(t, f.db, `DROP TRIGGER synthetic_media_fail_ready ON owned_media_files; DROP FUNCTION synthetic_media_fail_ready()`)
	if _, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("synthetic-fail-ready", "hello", false), strings.NewReader("hello")); e == nil {
		t.Fatal("SQL failure hidden")
	}
	mediaUsage(t, f, 0, 0)
	entries, e := os.ReadDir(f.root)
	if e != nil || len(entries) != 0 {
		t.Fatal("object survived rollback cleanup", entries, e)
	}
	var state string
	if e = f.db.QueryRow(`SELECT state FROM owned_media_files WHERE account_id=$1 AND idempotency_key='synthetic-fail-ready'`, f.actor).Scan(&state); e != nil || state != "deleted" {
		t.Fatal(state, e)
	}
}

func TestPGMediaRestartPersistence(t *testing.T) {
	db := database(t)
	phase := os.Getenv("PG_TEST_PHASE")
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	objects := filepath.Join(root, "media-objects")
	record := filepath.Join(root, "media-file.json")
	if phase == "before" {
		f := newMediaFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.root = objects
		f = mediaPool(t, f, db)
		file, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("restart", "durable-media", true), strings.NewReader("durable-media"))
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(file)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(record, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if phase != "after" {
		t.Fatal("restart test requires explicit before/after phase")
	}
	b, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	var file media.File
	if e = json.Unmarshal(b, &file); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	now := &atomic.Int64{}
	now.Store(time.Now().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	r, e := mediapg.New(db, store, authoritypg.NewTransactionGuard(authoritypg.New(db), v), mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	mediaRead(t, r, context.Background(), "", file.ID, "durable-media")
	var used, reserved int64
	if e = db.QueryRow(`SELECT used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, file.AccountID).Scan(&used, &reserved); e != nil || used != 13 || reserved != 0 {
		t.Fatal(used, reserved, e)
	}
}
