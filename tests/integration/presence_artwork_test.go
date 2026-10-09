// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/artwork"
)

type artworkFixture struct {
	mediaFixture
	app  *application.Backend
	data []byte
}

type artworkBlockingStore struct {
	media.ObjectStore
	entered, release chan struct{}
}

func (s artworkBlockingStore) Put(ctx context.Context, id string, reader io.Reader, size int64, hash string) error {
	if e := s.ObjectStore.Put(ctx, id, reader, size, hash); e != nil {
		return e
	}
	close(s.entered)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type artworkBlockingReadStore struct {
	media.ObjectStore
	entered, release chan struct{}
}

func (s artworkBlockingReadStore) Open(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	body, e := s.ObjectStore.Open(ctx, id)
	if e != nil {
		return nil, e
	}
	close(s.entered)
	select {
	case <-s.release:
		return body, nil
	case <-ctx.Done():
		body.Close()
		return nil, ctx.Err()
	}
}

func newArtworkFixture(t *testing.T) artworkFixture {
	t.Helper()
	f := artworkFixture{mediaFixture: newMediaFixture(t)}
	for _, id := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "media.files.*"}, {Pattern: artwork.ManagePermission}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	f.data = artworkPNG(t, f.actor)
	artworkApp(t, &f)
	return f
}

// Each fixture has unique real image bytes because the artwork hash is global,
// while synthetic fixtures deliberately use separate object directories.
func artworkPNG(t *testing.T, actor string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, len(actor), 1))
	for i := range actor {
		img.SetNRGBA(i, 0, color.NRGBA{R: actor[i], G: 127, B: 255, A: 255})
	}
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

func artworkApp(t *testing.T, f *artworkFixture) {
	t.Helper()
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	var e error
	f.app, e = application.NewPostgres(f.db, nil, application.Config{Authority: f.config, SessionService: f.issuer, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func artworkPost(t *testing.T, h http.Handler, token string, data []byte) artwork.Artwork {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="synthetic-artwork.png"`)
	hdr.Set("Content-Type", "image/png")
	part, e := w.CreatePart(hdr)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = part.Write(data); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "/api/presence/artworks", &body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", w.FormDataContentType())
	res := httptest.NewRecorder()
	h.ServeHTTP(res, r)
	return backendDecode[artwork.Artwork](t, requireArtworkStatus(t, res, 200))
}
func requireArtworkStatus(t *testing.T, w *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
	t.Helper()
	if w.Code != status {
		t.Fatal(w.Code, w.Body.String())
	}
	return w
}
func artworkFile(t *testing.T, f artworkFixture, hash string) string {
	t.Helper()
	var id string
	if e := f.db.QueryRow(`SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1::text`, hash).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}

func TestPGArtworkComposedUploadDedupOwnershipAndRemoval(t *testing.T) {
	f := newArtworkFixture(t)
	data := f.data
	a := artworkPost(t, f.app, f.token, data)
	id := artworkFile(t, f, a.Hash)
	if a.Hash != "sha256:"+mediaDigest(string(data)) || a.Size != int64(len(data)) || a.MIMEType != "image/png" || a.URL != "/api/presence/artworks/"+a.Hash {
		t.Fatal(a)
	}
	if w := backendCall(t, f.app, "GET", a.URL, "", "", 200); !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("artwork bytes differ")
	}
	backendCall(t, f.app, "GET", "/api/presence/artworks/"+strings.ToUpper(strings.TrimPrefix(a.Hash, "sha256:")), "", "", 200)
	b := artworkPost(t, f.app, f.otherToken, data)
	if a != b || artworkFile(t, f, a.Hash) != id {
		t.Fatal("global dedup", a, b)
	}
	mediaUsage(t, f.mediaFixture, int64(len(data)), 0)
	q, e := f.repo.Quota(f.otherCtx, f.other)
	if e != nil || q.UsedBytes != 0 || q.ReservedBytes != 0 {
		t.Fatal(q, e)
	}
	backendCall(t, f.app, "GET", "/api/media/files/"+id, f.otherToken, "", 404)
	backendCall(t, f.app, "DELETE", "/api/media/files/"+id, f.otherToken, "", 404)
	var owner string
	var public bool
	if e = f.db.QueryRow(`SELECT account_id::text,is_public FROM owned_media_files WHERE id=$1::uuid`, id).Scan(&owner, &public); e != nil || owner != f.actor || public {
		t.Fatal("dedup transferred ownership/public flag", owner, public, e)
	}
	if _, e = f.repo.Delete(f.ctx, f.actor, id); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", a.URL, "", "", 404)
	mediaUsage(t, f.mediaFixture, 0, 0)
	recreated := artworkPost(t, f.app, f.token, data)
	if recreated != a || artworkFile(t, f, a.Hash) == id {
		t.Fatal("deleted hash cannot be uploaded again", recreated)
	}
}

func TestPGArtworkExpiryTouchCleanupAndQuota(t *testing.T) {
	f := newArtworkFixture(t)
	data := f.data
	a := artworkPost(t, f.app, f.token, data)
	expire := func() {
		query(t, f.db, `UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp()-interval '31 days' WHERE hash=$1::text`, a.Hash)
	}
	expire()
	backendCall(t, f.app, "GET", a.URL, "", "", 404)
	if again := artworkPost(t, f.app, f.token, data); again != a {
		t.Fatal(again)
	}
	backendCall(t, f.app, "GET", a.URL, "", "", 200)
	expire()
	id := artworkFile(t, f, a.Hash)
	if n, e := f.app.CleanupMedia(context.Background(), 100); e != nil || n < 1 {
		t.Fatal(n, e)
	}
	backendCall(t, f.app, "GET", a.URL, "", "", 404)
	mediaUsage(t, f.mediaFixture, 0, 0)
	if _, e := os.Stat(filepath.Join(f.root, id)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("expired object retained", e)
	}
	if n, e := f.app.CleanupMedia(context.Background(), 100); e != nil || n != 0 {
		t.Fatal("cleanup replay", n, e)
	}
}

func TestPGArtworkConcurrentDuplicatesAndRollback(t *testing.T) {
	f := newArtworkFixture(t)
	data := f.data
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.repo.UploadArtwork(f.ctx, f.actor, "image/png", data); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil && !errors.Is(e, media.ErrPending) {
			t.Fatal(e)
		}
	}
	a := artworkPost(t, f.app, f.token, data)
	mediaUsage(t, f.mediaFixture, int64(len(data)), 0)
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_media_files WHERE account_id=$1::uuid AND state='ready'`, f.actor).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate files", count, e)
	}
	constraint := "artwork_rollback_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_presence_artworks ADD CONSTRAINT `+constraint+` CHECK(hash='`+a.Hash+`')NOT VALID`)
	t.Cleanup(func() { f.db.Exec(`ALTER TABLE owned_presence_artworks DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.UploadArtwork(f.ctx, f.actor, "image/png", append(data, 1)); e == nil {
		t.Fatal("injected mapping failure succeeded")
	}
	query(t, f.db, `ALTER TABLE owned_presence_artworks DROP CONSTRAINT `+constraint)
	mediaUsage(t, f.mediaFixture, int64(len(data)), 0)
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_media_files WHERE account_id=$1::uuid`, f.actor).Scan(&count); e != nil || count != 1 {
		t.Fatal("failed mapping retained reservation", count, e)
	}
}

func TestPGArtworkWaitRechecksOwnerExpiryAndAuthority(t *testing.T) {
	for _, change := range []string{"disable", "expire", "delete"} {
		t.Run(change, func(t *testing.T) {
			f := newArtworkFixture(t)
			a := artworkPost(t, f.app, f.token, f.data)
			id := artworkFile(t, f, a.Hash)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", id)
			done := make(chan error, 1)
			go func() {
				_, body, e := f.repo.OpenArtwork(ctx, "", a.Hash)
				if body != nil {
					body.Close()
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			switch change {
			case "disable":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
			case "expire":
				query(t, f.db, `UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp()-interval '31 days' WHERE hash=$1::text`, a.Hash)
			case "delete":
				if _, e := blocker.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting' WHERE id=$1::uuid`, id); e != nil {
					t.Fatal(e)
				}
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, media.ErrNotFound) {
				t.Fatal("stale artwork read after wait", e)
			}
		})
	}
	for _, operation := range []string{"read-after-storage-wait", "dedup-after-storage-wait"} {
		t.Run(operation, func(t *testing.T) {
			f := newArtworkFixture(t)
			a := artworkPost(t, f.app, f.token, f.data)
			ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
			defer cancel()
			blocked := artworkBlockingReadStore{ObjectStore: f.store, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(blocked.release) }) }
			defer release()
			repo, e := mediapg.New(f.db, blocked, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "dedup-after-storage-wait" {
					_, e := repo.UploadArtwork(ctx, f.other, "image/png", f.data)
					done <- e
					return
				}
				_, body, e := repo.OpenArtwork(ctx, f.other, a.Hash)
				if body != nil {
					body.Close()
				}
				done <- e
			}()
			select {
			case <-blocked.entered:
			case <-ctx.Done():
				t.Fatal("artwork did not reach storage read", ctx.Err())
			}
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
			release()
			if e = <-done; !errors.Is(e, media.ErrNotFound) {
				t.Fatal("disabled owner accepted after storage wait", e)
			}
		})
	}
	f := newArtworkFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid AND pattern=$2::text`, f.actor, artwork.ManagePermission); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.repo.UploadArtwork(ctx, f.actor, "image/png", f.data); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("revoked artwork grant used", e)
	}
	mediaUsage(t, f.mediaFixture, 0, 0)

	// Bytes are already durable when the artwork-specific grant expires. The
	// final check must reject publication and the canonical worker must release
	// quota only after removing those bytes.
	g := newArtworkFixture(t)
	var now atomic.Int64
	now.Store(g.config.Now().Unix())
	cfg := g.config
	cfg.Now = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	expires := cfg.Now().Add(time.Minute)
	if e := authoritypg.New(g.db).ReplacePolicy(context.Background(), g.actor, []authority.Rule{{Pattern: "media.files.*"}, {Pattern: artwork.ManagePermission, ExpiresAt: &expires}}, nil); e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(authoritypg.New(g.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(g.db), v)
	request := httptest.NewRequest("POST", "/api/presence/artworks", nil)
	request.Header.Set("Authorization", "Bearer "+g.token)
	proof, e := v.AuthenticateRequest(request)
	if e != nil {
		t.Fatal(e)
	}
	streamCtx, stop := context.WithTimeout(guard.Bind(context.Background(), proof), 10*time.Second)
	defer stop()
	blocked := artworkBlockingStore{ObjectStore: g.store, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release) }) }
	defer release()
	repo, e := mediapg.New(g.db, blocked, guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	payload := g.data
	finished := make(chan error, 1)
	go func() { _, e := repo.UploadArtwork(streamCtx, g.actor, "image/png", payload); finished <- e }()
	select {
	case <-blocked.entered:
	case <-streamCtx.Done():
		t.Fatal("object write did not reach final authorization", streamCtx.Err())
	}
	now.Add(120)
	release()
	if e = <-finished; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("expired artwork grant published", e)
	}
	mediaUsage(t, g.mediaFixture, 0, 0)
	if entries, e := os.ReadDir(g.root); e != nil || len(entries) != 0 {
		t.Fatal("rejected publication retained object bytes", entries, e)
	}
	if _, body, e := g.repo.OpenArtwork(context.Background(), "", "sha256:"+mediaDigest(string(payload))); !errors.Is(e, media.ErrNotFound) || body != nil {
		if body != nil {
			body.Close()
		}
		t.Fatal("rejected publication readable", e)
	}
}

func TestPGArtworkRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	objects := restartFixturePath(t, "presence-artwork-objects")
	const marker = "presence-artwork-record.json"
	type saved struct {
		Artwork     artwork.Artwork
		Actor, File string
		Data        []byte
	}
	if phase == "before" {
		f := newArtworkFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.mediaFixture.root = objects
		f.mediaFixture = mediaPool(t, f.mediaFixture, f.db)
		artworkApp(t, &f)
		a := artworkPost(t, f.app, f.token, f.data)
		saveRestartFixture(t, marker, saved{a, f.actor, artworkFile(t, f, a.Hash), f.data})
		return
	}
	var state saved
	loadRestartFixture(t, marker, &state)
	store, e := mediastore.NewFilesystem(objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	var now atomic.Int64
	now.Store(time.Now().Unix())
	cfg := authorityConfig(t, authorityKeys(t), &now)
	verifier, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	repo, e := mediapg.New(db, store, authoritypg.NewTransactionGuard(authoritypg.New(db), verifier), mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.ArtworkReady(context.Background()); e != nil {
		t.Fatal(e)
	}
	a, body, e := repo.OpenArtwork(context.Background(), "", state.Artwork.Hash)
	if e != nil {
		t.Fatal(e)
	}
	defer body.Close()
	content, e := io.ReadAll(body)
	if e != nil || a != state.Artwork || !bytes.Equal(content, state.Data) {
		t.Fatal("artwork restart projection or bytes", a, e)
	}
	var owner, file string
	var public bool
	if e = db.QueryRow(`SELECT f.account_id::text,f.id::text,f.is_public FROM owned_presence_artworks a JOIN owned_media_files f ON f.id=a.file_id WHERE a.hash=$1::text`, state.Artwork.Hash).Scan(&owner, &file, &public); e != nil || owner != state.Actor || file != state.File || public {
		t.Fatal("artwork restart changed canonical ownership", owner, file, public, e)
	}
	var used, reserved int64
	if e = db.QueryRow(`SELECT used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1::uuid`, state.Actor).Scan(&used, &reserved); e != nil || used != int64(len(state.Data)) || reserved != 0 {
		t.Fatal("artwork restart charge", used, reserved, e)
	}
}
