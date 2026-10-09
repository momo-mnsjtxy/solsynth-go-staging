// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
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

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/media"
)

func miniappMultipart(t *testing.T, h http.Handler, token, target string, data []byte, extra bool, want int) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	p, e := w.CreateFormFile("File", "synthetic-package.zip")
	if e != nil {
		t.Fatal(e)
	}
	p.Write(data)
	if extra {
		w.WriteField("unrecognized", "not accepted")
	}
	w.Close()
	r := httptest.NewRequest("POST", target, &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != want {
		t.Fatalf("multipart status %d want %d: %s", out.Code, want, out.Body.String())
	}
	return out
}
func miniappMedia(t *testing.T, f botFixture, root string) (*mediapg.Repository, *mediastore.Filesystem) {
	t.Helper()
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(root, 6<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	m, e := mediapg.New(f.db, store, f.guard, mediapg.Config{MaxFileBytes: 5 << 20, PendingTTL: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{f.actor, f.other} {
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1,20000000)ON CONFLICT(account_id)DO NOTHING`, id)
	}
	f.repo.WithMedia(m)
	m.WithReadVisibility(f.repo)
	return m, store
}
func miniappUpload(t *testing.T, f botFixture, id string, data []byte) bots.PackageResult {
	t.Helper()
	v, e := f.repo.UploadPackage(f.ctx, f.scope, id, "synthetic.zip", bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func miniappImage(t *testing.T, f botFixture, m *mediapg.Repository, actor string) media.File {
	t.Helper()
	token := f.token
	if actor == f.other {
		token = f.otherToken
	}
	p, e := f.v.Authenticate(f.ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	data := profilePNG(t)
	v, _, e := m.Upload(f.guard.Bind(f.ctx, p), actor, media.Upload{Name: "inert.png", MIMEType: "image/png", Size: int64(len(data)), SHA256: mediaDigest(string(data)), IdempotencyKey: "miniapp-image-" + authorityUUID(t)}, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func miniappAuthoredTarget(f botFixture, id string) string {
	return "/api/private/miniapps/" + id + "/package?dev=" + f.dev + "&proj=" + f.scope.Project
}

func TestPGMiniappMultipartAuthoredShapeAndReplacement(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "upload")
	m, _ := miniappMedia(t, f, t.TempDir())
	firstBytes := botZip(t, authorityUUID(t))
	out := miniappMultipart(t, f.http, f.token, miniappAuthoredTarget(f, mini.ID), firstBytes, false, 200)
	first := backendDecode[bots.PackageResult](t, out)
	var shape map[string]any
	if json.Unmarshal(out.Body.Bytes(), &shape) != nil || len(shape) != 6 || first.FileName != "synthetic-package.zip" || first.Size != int64(len(firstBytes)) || first.SHA256 != mediaDigest(string(firstBytes)) {
		t.Fatal(shape, first)
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_media_files WHERE id=$1 AND state='ready' AND NOT is_public`, first.Key); n != 1 {
		t.Fatal("package became globally public", n)
	}
	secondBytes := botZip(t, authorityUUID(t))
	second := miniappUpload(t, f, mini.ID, secondBytes)
	if second.Key == first.Key {
		t.Fatal("replacement reused old identity")
	}
	w := backendCall(t, f.http, "GET", "/api/miniapps/"+mini.Slug+"/package", "", "", 200)
	if !bytes.Equal(w.Body.Bytes(), secondBytes) {
		t.Fatal("stale package bytes")
	}
	if _, _, e := m.Open(context.Background(), "", first.Key); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("replaced package retained public grant", e)
	}
	if _, body, e := m.Open(f.ctx, f.actor, first.Key); e != nil {
		t.Fatal("previous user blob removed", e)
	} else {
		body.Close()
	}
	got, e := f.repo.PublicMiniApp(context.Background(), mini.Slug)
	if e != nil || got.PackageSHA256 == nil || *got.PackageSHA256 != second.SHA256 {
		t.Fatal(got, e)
	}
	if n := botCount(t, f, `SELECT used_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor); n != first.Size+second.Size {
		t.Fatal("incorrect canonical accounting", n)
	}
}
func TestPGMiniappMultipartQuotaMalformedAndAuditRollback(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "rollback")
	root := t.TempDir()
	_, _ = miniappMedia(t, f, root)
	data := botZip(t, authorityUUID(t))
	old := miniappUpload(t, f, mini.ID, data)
	miniappMultipart(t, f.http, f.token, miniappAuthoredTarget(f, mini.ID), data, true, 400)
	miniappMultipart(t, f.http, f.token, miniappAuthoredTarget(f, mini.ID), []byte("not a ZIP"), false, 400)
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=used_bytes WHERE account_id=$1`, f.actor)
	miniappMultipart(t, f.http, f.token, miniappAuthoredTarget(f, mini.ID), data, false, 403)
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=20000000 WHERE account_id=$1`, f.actor)
	constraint := "synthetic_miniapp_audit_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_bot_audit ADD CONSTRAINT `+constraint+` CHECK(actor_id<>'`+f.actor+`'::uuid)NOT VALID`)
	t.Cleanup(func() { f.db.Exec(`ALTER TABLE owned_bot_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.UploadPackage(f.ctx, f.scope, mini.ID, "failed.zip", bytes.NewReader(botZip(t, authorityUUID(t)))); e == nil {
		t.Fatal("audit failure committed")
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_media_files WHERE account_id=$1 AND state<>'deleted'`, f.actor); n != 1 {
		t.Fatal("rollback left a media row", n)
	}
	if n := botCount(t, f, `SELECT used_bytes+reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor); n != old.Size {
		t.Fatal("rollback leaked quota", n)
	}
	files, e := os.ReadDir(root)
	if e != nil || len(files) != 1 || files[0].Name() != old.Key {
		t.Fatal("cleanup removed old blob or retained task blob", files, e)
	}
	var current string
	if e = f.db.QueryRow(`SELECT package_file_id::text FROM owned_bot_miniapps WHERE id=$1`, mini.ID).Scan(&current); e != nil || current != old.Key {
		t.Fatal(current, e)
	}
}
func TestPGMiniappMediaCanonicalAssignmentClearAndPrivacy(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "media")
	m, _ := miniappMedia(t, f, t.TempDir())
	image := miniappImage(t, f, m, f.actor)
	foreign := miniappImage(t, f, m, f.other)
	target := "/api/private/miniapps/" + mini.ID + "?dev=" + f.dev + "&proj=" + f.scope.Project
	backendCall(t, f.http, "PATCH", target, f.token, `{"icon_id":"`+image.ID+`","background_id":"`+image.ID+`"}`, 200)
	got, e := f.repo.PublicMiniApp(context.Background(), mini.Slug)
	if e != nil || got.Icon == nil || got.Background == nil {
		t.Fatal(got, e)
	}
	if _, body, e := m.Open(context.Background(), "", image.ID); e != nil {
		t.Fatal(e)
	} else {
		body.Close()
	}
	backendCall(t, f.http, "PATCH", target, f.token, `{"icon_id":"`+foreign.ID+`"}`, 403)
	backendCall(t, f.http, "PATCH", target, f.token, `{"icon_id":"https://example.invalid/icon"}`, 400)
	backendCall(t, f.http, "PATCH", target, f.token, `{"icon_id":null}`, 200)
	got, e = f.repo.PublicMiniApp(context.Background(), mini.Slug)
	if e != nil || got.Icon == nil {
		t.Fatal("authored null should retain", e)
	}
	backendCall(t, f.http, "PATCH", target, f.token, `{"icon_id":"","background_id":"  "}`, 200)
	if _, _, e = m.Open(context.Background(), "", image.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("cleared association leaked", e)
	}
	backendCall(t, f.http, "PATCH", target, f.token, `{"icon_id":"`+image.ID+`"}`, 200)
	backendCall(t, f.http, "DELETE", target, f.token, "", 204)
	if _, _, e = m.Open(context.Background(), "", image.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("deleted miniapp media leaked", e)
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_miniapp_media WHERE miniapp_id=$1`, mini.ID); n != 0 {
		t.Fatal("deleted slots retained", n)
	}
}
func TestPGMiniappPackageAndMediaCurrentPublicationGates(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "privacy")
	m, _ := miniappMedia(t, f, t.TempDir())
	pack := miniappUpload(t, f, mini.ID, botZip(t, authorityUUID(t)))
	image := miniappImage(t, f, m, f.actor)
	if _, e := f.repo.MiniApps(f.ctx, f.scope, mini.ID, &bots.MiniPatch{IconID: &image.ID}, "update"); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ disable, restore, id string }{
		{`UPDATE owned_bot_miniapps SET stage=0 WHERE id=$1`, `UPDATE owned_bot_miniapps SET stage=2 WHERE id=$1`, mini.ID},
		{`UPDATE owned_custom_apps SET status=2 WHERE id=$1`, `UPDATE owned_custom_apps SET status=1 WHERE id=$1`, f.app.ID},
		{`UPDATE owned_dev_projects SET deleted_at=now() WHERE id=$1`, `UPDATE owned_dev_projects SET deleted_at=NULL WHERE id=$1`, f.scope.Project},
		{`UPDATE owned_publishers SET deleted_at=now() WHERE id=$1`, `UPDATE owned_publishers SET deleted_at=NULL WHERE id=$1`, f.app.PublisherID},
		{`UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor},
	} {
		query(t, f.db, tc.disable, tc.id)
		if _, _, e := f.repo.OpenPackage(context.Background(), mini.Slug); !errors.Is(e, bots.ErrNotFound) {
			t.Fatal("hidden package", e)
		}
		for _, id := range []string{pack.Key, image.ID} {
			if _, _, e := m.Open(context.Background(), "", id); !errors.Is(e, media.ErrNotFound) {
				t.Fatal("raw media route leaked private association", e)
			}
		}
		query(t, f.db, tc.restore, tc.id)
		if _, body, e := m.Open(context.Background(), "", image.ID); e != nil {
			t.Fatal("restore", e)
		} else {
			body.Close()
		}
	}
}

type miniappHookStore struct {
	media.ObjectStore
	afterPut   func()
	beforeOpen func()
}

func (s miniappHookStore) Put(ctx context.Context, id string, r io.Reader, n int64, hash string) error {
	e := s.ObjectStore.Put(ctx, id, r, n, hash)
	if e == nil && s.afterPut != nil {
		s.afterPut()
	}
	return e
}
func (s miniappHookStore) Open(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	if s.beforeOpen != nil {
		s.beforeOpen()
	}
	return s.ObjectStore.Open(ctx, id)
}
func TestPGMiniappUploadOriginalProofAfterStorageWait(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "expiry")
	root := t.TempDir()
	_, store := miniappMedia(t, f, root)
	clock := &atomic.Int64{}
	clock.Store(f.config.Now().Unix())
	cfg := f.config
	cfg.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	proof, e := verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := guard.Bind(f.ctx, proof)
	hook := miniappHookStore{ObjectStore: store, afterPut: func() { clock.Add(48 * 60 * 60) }}
	m, e := mediapg.New(f.db, hook, guard, mediapg.Config{MaxFileBytes: 5 << 20, PendingTTL: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	repo := botpg.NewWithAuthority(f.db, guard).WithMedia(m)
	m.WithReadVisibility(repo)
	if _, e = repo.UploadPackage(ctx, f.scope, mini.ID, "expired.zip", bytes.NewReader(botZip(t, authorityUUID(t)))); e == nil {
		t.Fatal("expired original proof published")
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_media_files WHERE account_id=$1 AND state<>'deleted'`, f.actor); n != 0 {
		t.Fatal("expired storage commit", n)
	}
	files, e := os.ReadDir(root)
	if e != nil || len(files) != 0 {
		t.Fatal("new storage blob not removed", files, e)
	}
	if n := botCount(t, f, `SELECT used_bytes+reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal("quota survived expiry", n)
	}
}
func TestPGMiniappUploadWaitRevocationAndConcurrentReplacement(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "race")
	_, _ = miniappMedia(t, f, t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		data := botZip(t, authorityUUID(t))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.repo.UploadPackage(f.ctx, f.scope, mini.ID, "race.zip", bytes.NewReader(data))
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_media_files WHERE account_id=$1 AND state='ready'`, f.actor); n != 4 {
		t.Fatal(n)
	}
	var current string
	if e := f.db.QueryRow(`SELECT package_file_id::text FROM owned_bot_miniapps WHERE id=$1`, mini.ID).Scan(&current); e != nil {
		t.Fatal(e)
	}
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.actor}
	blocker, pid := atomicBlocker(t, base, "owned_custom_apps", f.app.ID)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE account_id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	data := botZip(t, authorityUUID(t))
	done := make(chan error, 1)
	go func() {
		_, e := f.repo.UploadPackage(f.ctx, f.scope, mini.ID, "revoked.zip", bytes.NewReader(data))
		done <- e
	}()
	waitAtomicBlocked(t, base, pid, "owned_custom_apps")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked waiter committed", e)
	}
	var after string
	if e := f.db.QueryRow(`SELECT package_file_id::text FROM owned_bot_miniapps WHERE id=$1`, mini.ID).Scan(&after); e != nil || after != current {
		t.Fatal(after, current, e)
	}
}
func TestPGBackendMiniappAuthoredMultipartAndCanonicalMedia(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "composed")
	_, store := miniappMedia(t, f, t.TempDir())
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: store, MaxMediaBytes: 5 << 20})
	if e != nil {
		t.Fatal(e)
	}
	data := botZip(t, authorityUUID(t))
	target := "/develop/private/miniapps/" + mini.ID + "/package?dev=" + f.dev + "&proj=" + f.scope.Project
	got := backendDecode[bots.PackageResult](t, miniappMultipart(t, app, f.token, target, data, false, 200))
	image := profileUpload(t, app, f.token, "image/png", profilePNG(t))
	backendCall(t, app, "PATCH", "/develop/private/miniapps/"+mini.ID+"?dev="+f.dev+"&proj="+f.scope.Project, f.token, `{"icon_id":"`+image+`"}`, 200)
	if w := backendCall(t, app, "GET", "/develop/miniapps/"+mini.Slug+"/package", "", "", 200); !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("wrong composed package")
	}
	backendCall(t, app, "GET", "/api/files/"+got.Key, "", "", 200)
	backendCall(t, app, "GET", "/api/files/"+image, "", "", 200)
	query(t, f.db, `UPDATE owned_bot_miniapps SET stage=0 WHERE id=$1`, mini.ID)
	backendCall(t, app, "GET", "/api/files/"+got.Key, "", "", 404)
	backendCall(t, app, "GET", "/api/files/"+image, "", "", 404)
}
func TestPGMiniappUploadMediaSavedRestartPersistence(t *testing.T) {
	db := database(t)
	phase := fixtureRestartPhase(t)
	const marker = "synthetic-miniapp-upload-restart.json"
	objects := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-miniapp-upload-store")
	type state struct {
		Mini, Slug, File, Hash, Image, Actor, Replaced, ReplacedHash, Token, Session string
		Size                                                                         int64
		Scope                                                                        apps.Scope
		Authority                                                                    restartAuthoritySnapshot
	}
	var saved state
	if phase == "before" {
		f := newBotFixture(t)
		// Issue one bounded original proof before the target miniapp exists. Only
		// public verification material and this synthetic token enter the private
		// exclusive marker. The after phase cannot issue or prolong credentials.
		at, wall := f.config.Now(), time.Now()
		f.config.AccessTTL, f.config.SessionTTL = time.Hour, time.Hour
		f.config.Now = func() time.Time { return at.Add(time.Since(wall)) }
		issuer, e := authority.New(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		pair := pgIssue(t, issuer, f.actor)
		f.token = pair.Token
		f.v, e = authority.NewVerifier(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.v)
		proof, e := f.v.Authenticate(f.ctx, f.token)
		if e != nil {
			t.Fatal(e)
		}
		f.ctx = f.guard.Bind(f.ctx, proof)
		f.repo = botpg.NewWithAuthority(f.db, f.guard)
		mini := botMini(t, f, "saved")
		m, _ := miniappMedia(t, f, objects)
		old := miniappUpload(t, f, mini.ID, botZip(t, authorityUUID(t)))
		pack := miniappUpload(t, f, mini.ID, botZip(t, authorityUUID(t)))
		image := miniappImage(t, f, m, f.actor)
		if _, e = f.repo.MiniApps(f.ctx, f.scope, mini.ID, &bots.MiniPatch{IconID: &image.ID}, "update"); e != nil {
			t.Fatal(e)
		}
		saved = state{Mini: mini.ID, Slug: mini.Slug, File: pack.Key, Hash: pack.SHA256, Image: image.ID, Actor: f.actor, Replaced: old.Key, ReplacedHash: old.SHA256, Token: f.token, Session: proof.Session.ID, Size: pack.Size, Scope: f.scope, Authority: saveAuthoritySnapshot(t, f.config)}
		saveRestartFixture(t, marker, saved)
	} else {
		loadRestartFixture(t, marker, &saved)
	}
	store, e := mediastore.NewFilesystem(objects, 6<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	config := restoreAuthoritySnapshot(t, saved.Authority)
	verifier, e := authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), saved.Token)
	if e != nil || proof.Session.ID != saved.Session || proof.Account.ID != saved.Actor {
		t.Fatal("original proof failed", e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	ctx := guard.Bind(context.Background(), proof)
	m, e := mediapg.New(db, store, guard, mediapg.Config{MaxFileBytes: 5 << 20, PendingTTL: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	repo := botpg.NewWithAuthority(db, guard).WithMedia(m)
	m.WithReadVisibility(repo)
	file, body, e := repo.OpenPackage(context.Background(), saved.Slug)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(body)
	body.Close()
	if e != nil || file.ID != saved.File || file.Size != saved.Size || mediaDigest(string(data)) != saved.Hash {
		t.Fatal("saved package changed", e)
	}
	mini, e := repo.PublicMiniApp(context.Background(), saved.Slug)
	if e != nil || mini.ID != saved.Mini || mini.Icon == nil {
		t.Fatal("saved media link missing", mini, e)
	}
	icon, body, e := m.Open(context.Background(), "", saved.Image)
	if e != nil {
		t.Fatal(e)
	}
	body.Close()
	if icon.AccountID != saved.Actor || icon.Public {
		t.Fatal("saved ownership changed", icon)
	}
	if _, body, e = m.Open(context.Background(), "", saved.Replaced); !errors.Is(e, media.ErrNotFound) || body != nil {
		t.Fatal("saved replaced package exposed", e)
	}
	old, body, e := m.Open(ctx, saved.Actor, saved.Replaced)
	if e != nil {
		t.Fatal("original proof private read failed", e)
	}
	oldBytes, e := io.ReadAll(body)
	body.Close()
	if e != nil || old.SHA256 != saved.ReplacedHash || mediaDigest(string(oldBytes)) != saved.ReplacedHash {
		t.Fatal("saved owner bytes changed", e)
	}
	if phase == "after" {
		private := 0
		if _, e = repo.MiniApps(ctx, saved.Scope, saved.Mini, &bots.MiniPatch{Stage: &private}, "update"); e != nil {
			t.Fatal("saved proof private management", e)
		}
		uploaded, e := repo.UploadPackage(ctx, saved.Scope, saved.Mini, "after-restart.zip", bytes.NewReader(data))
		if e != nil {
			t.Fatal("saved original proof upload", e)
		}
		if _, body, e = m.Open(context.Background(), "", uploaded.Key); !errors.Is(e, media.ErrNotFound) || body != nil {
			t.Fatal("private restart upload leaked", e)
		}
		if _, body, e = m.Open(ctx, saved.Actor, uploaded.Key); e != nil {
			t.Fatal("saved owner upload read", e)
		} else {
			body.Close()
		}
		query(t, db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp(),epoch=epoch+1 WHERE id=$1`, saved.Session)
		if _, e = repo.UploadPackage(ctx, saved.Scope, saved.Mini, "revoked-after-restart.zip", bytes.NewReader(data)); !errors.Is(e, authority.ErrRevoked) {
			t.Fatal("saved revoked proof uploaded", e)
		}
		var current string
		if e = db.QueryRow(`SELECT package_file_id::text FROM owned_bot_miniapps WHERE id=$1`, saved.Mini).Scan(&current); e != nil || current != uploaded.Key {
			t.Fatal("revocation changed package", e)
		}
	}
}

func TestPGMiniappPendingReservationRecoveryAndCommitCleanup(t *testing.T) {
	t.Run("expired-original-proof-after-quota-wait", miniappReservationExpiredAfterQuotaWait)
	f := newBotFixture(t)
	m, store := miniappMedia(t, f, t.TempDir())
	data := botZip(t, authorityUUID(t))
	u := media.Upload{Name: "recovery.zip", MIMEType: "application/zip", Size: int64(len(data)), SHA256: mediaDigest(string(data))}
	pending, _, e := m.ReserveParentUpload(f.ctx, f.actor, u)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Put(f.ctx, pending.ID, bytes.NewReader(data), pending.Size, pending.SHA256); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_media_files SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, pending.ID)
	if n, e := m.Cleanup(f.ctx, 1000); e != nil || n < 1 {
		t.Fatal("abandoned reservation recovery", n, e)
	}
	if _, e = store.Open(f.ctx, pending.ID); e == nil {
		t.Fatal("abandoned object retained")
	}
	reserved, cleanup, e := m.ReserveParentUpload(f.ctx, f.actor, u)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	file, e := m.FinishParentUpload(f.ctx, tx, f.actor, reserved, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = cleanup(f.ctx); e != nil {
		t.Fatal(e)
	}
	if got, body, e := m.Open(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal("cleanup removed committed object", e)
	} else {
		body.Close()
		if got.State != "ready" {
			t.Fatal(got)
		}
	}
	if n := botCount(t, f, `SELECT used_bytes+reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor); n != file.Size {
		t.Fatal("cleanup quota mismatch", n)
	}
}
func TestPGMiniappMediaReadRechecksPublicationAfterStorage(t *testing.T) {
	f := newBotFixture(t)
	mini := botMini(t, f, "readwait")
	m, store := miniappMedia(t, f, t.TempDir())
	image := miniappImage(t, f, m, f.actor)
	if _, e := f.repo.MiniApps(f.ctx, f.scope, mini.ID, &bots.MiniPatch{IconID: &image.ID}, "update"); e != nil {
		t.Fatal(e)
	}
	hook := miniappHookStore{ObjectStore: store, beforeOpen: func() { query(t, f.db, `UPDATE owned_bot_miniapps SET stage=0 WHERE id=$1`, mini.ID) }}
	reader, e := mediapg.New(f.db, hook, f.guard, mediapg.Config{MaxFileBytes: 5 << 20, PendingTTL: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	reader.WithReadVisibility(f.repo)
	if _, body, e := reader.Open(context.Background(), "", image.ID); !errors.Is(e, media.ErrNotFound) || body != nil {
		t.Fatal("stale published media escaped storage wait", body, e)
	}
}
func TestPGMiniappCreateMediaAtomicityAndScopedPermissions(t *testing.T) {
	f := newBotFixture(t)
	m, _ := miniappMedia(t, f, t.TempDir())
	image := miniappImage(t, f, m, f.actor)
	foreign := miniappImage(t, f, m, f.other)
	target := "/api/private/miniapps?dev=" + f.dev + "&proj=" + f.scope.Project
	slug := "scoped-mini-" + f.actor
	backendCall(t, f.http, "POST", target, f.token, `{"slug":"`+slug+`","icon_id":"`+foreign.ID+`"}`, 403)
	if n := botCount(t, f, `SELECT count(*) FROM owned_bot_miniapps WHERE slug=$1`, slug); n != 0 {
		t.Fatal("failed media assignment left app", n)
	}
	mini := backendDecode[bots.MiniApp](t, backendCall(t, f.http, "POST", target, f.token, `{"slug":"`+slug+`","icon_id":"`+image.ID+`"}`, 201))
	if mini.Icon == nil {
		t.Fatal("create lost icon")
	}
	issuer, e := authority.New(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	limited, e := issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic miniapp package only", Scopes: []string{"developers.miniapps.package.upload"}})
	if e != nil {
		t.Fatal(e)
	}
	miniappMultipart(t, f.http, limited.Token, miniappAuthoredTarget(f, mini.ID), botZip(t, authorityUUID(t)), false, 403)
	enough, e := issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic exact upload permissions", Scopes: []string{"developers.miniapps.package.upload", "media.files.create"}})
	if e != nil {
		t.Fatal(e)
	}
	miniappMultipart(t, f.http, enough.Token, miniappAuthoredTarget(f, mini.ID), botZip(t, authorityUUID(t)), false, 200)
	if _, _, e = m.Open(context.Background(), "", image.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("development icon leaked", e)
	}
}
