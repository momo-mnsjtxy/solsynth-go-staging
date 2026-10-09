// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/developmediapg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/media"
)

type developerMediaFixture struct {
	botFixture
	images *developmediapg.Repository
	files  *mediapg.Repository
	host   *application.Backend
	store  *mediastore.Filesystem
	root   string
}

func newDeveloperMediaFixture(t *testing.T, root string) developerMediaFixture {
	t.Helper()
	f := developerMediaFixture{botFixture: newBotFixture(t)}
	query(t, f.db, developmediapg.Schema)
	if root == "" {
		root = t.TempDir()
	}
	f.root = root
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	var e error
	f.store, e = mediastore.NewFilesystem(root, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.store.Close() })
	f.images = developmediapg.New(f.db)
	f.repo.WithProfileMedia(f.images)
	f.developerFixture.repo.WithMedia(f.images)
	f.files, e = mediapg.New(f.db, f.store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	f.files.WithReadVisibility(f.images)
	for _, id := range []string{f.actor, f.other} {
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1,1048576)ON CONFLICT DO NOTHING`, id)
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	f.host, e = application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.host.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return f
}
func developerImage(t *testing.T, f developerMediaFixture, actor string, public bool, mime string) media.File {
	t.Helper()
	token := f.token
	if actor == f.other {
		token = f.otherToken
	}
	proof, e := f.v.Authenticate(f.ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	data := profilePNG(t)
	v, _, e := f.files.Upload(f.guard.Bind(f.ctx, proof), actor, media.Upload{Name: "synthetic.png", MIMEType: mime, Size: int64(len(data)), SHA256: mediaDigest(string(data)), Public: public, IdempotencyKey: authorityUUID(t)}, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func developerAppPath(f developerMediaFixture) string {
	return "/api/private/apps/" + f.app.ID + "?dev=" + f.dev + "&proj=" + f.scope.Project
}
func developerProduct(t *testing.T, f developerMediaFixture, file string) apps.Product {
	return backendDecode[apps.Product](t, backendJSONCall(t, f.host, "POST", developerAppPath(f)[:strings.Index(developerAppPath(f), "?")]+"/products?dev="+f.dev+"&proj="+f.scope.Project, f.token, map[string]any{"identifier": "synthetic-image-product", "display_name": "Synthetic image product", "currency": "usd", "picture_id": file}, 201))
}
func TestPGDeveloperMediaComposedAppProductBotLifecycle(t *testing.T) {
	f := newDeveloperMediaFixture(t, "")
	picture := developerImage(t, f, f.actor, false, "image/png")
	background := developerImage(t, f, f.actor, false, "image/png")
	a := backendDecode[apps.App](t, backendJSONCall(t, f.host, "PATCH", developerAppPath(f), f.token, map[string]any{"picture_id": picture.ID, "background_id": background.ID}, 200))
	if a.Picture["id"] != picture.ID || a.Background["id"] != background.ID {
		t.Fatal(a)
	}
	product := developerProduct(t, f, picture.ID)
	if product.Picture["id"] != picture.ID {
		t.Fatal(product)
	}
	bot := backendDecode[bots.Bot](t, backendJSONCall(t, f.host, "POST", "/api/private/bots"+f.query, f.token, map[string]any{"slug": "image-bot", "name": "image_bot_" + strings.ReplaceAll(f.actor, "-", ""), "nick": "Image bot", "picture_id": picture.ID, "background_id": background.ID}, 200))
	if bot.Account.Profile.Picture["id"] != picture.ID || bot.Account.Profile.Background["id"] != background.ID {
		t.Fatal(bot)
	}
	public := backendDecode[apps.App](t, backendCall(t, f.host, "GET", "/api/apps/"+a.Slug, "", "", 200))
	if public.Picture["id"] != picture.ID {
		t.Fatal(public)
	}
	publicBot := backendDecode[bots.Bot](t, backendCall(t, f.host, "GET", "/api/bots/"+bot.ID, "", "", 200))
	if publicBot.Account.Profile.Picture["id"] != picture.ID {
		t.Fatal(publicBot)
	}
	backendCall(t, f.host, "GET", "/api/files/"+picture.ID, "", "", 200)
	a = backendDecode[apps.App](t, backendJSONCall(t, f.host, "PATCH", developerAppPath(f), f.token, map[string]any{"picture_id": nil, "name": "Changed name"}, 200))
	if a.Picture["id"] != picture.ID {
		t.Fatal("source null must retain", a)
	}
	var publicFlag bool
	if e := f.db.QueryRow(`SELECT is_public FROM owned_media_files WHERE id=$1`, picture.ID).Scan(&publicFlag); e != nil || publicFlag {
		t.Fatal("association changed private flag", e)
	}
	// Read responses resolve metadata from the canonical file, not saved JSON.
	query(t, f.db, `UPDATE owned_media_files SET name='current-image.png' WHERE id=$1`, picture.ID)
	products := backendDecode[[]apps.Product](t, backendCall(t, f.host, "GET", "/api/apps/"+a.Slug+"/products", "", "", 200))
	if len(products) != 1 || products[0].Picture["name"] != "current-image.png" {
		t.Fatal(products)
	}
	backendCall(t, f.host, "DELETE", "/api/private/bots/"+bot.ID+f.query, f.token, "", 204)
	backendCall(t, f.host, "DELETE", developerAppPath(f)[:strings.Index(developerAppPath(f), "?")]+"/products/"+product.ID+"?dev="+f.dev+"&proj="+f.scope.Project, f.token, "", 204)
	backendCall(t, f.host, "DELETE", developerAppPath(f), f.token, "", 204)
	backendCall(t, f.host, "GET", "/api/files/"+picture.ID, "", "", 404)
	if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_developer_media WHERE resource_id IN($1,$2,$3)`, a.ID, product.ID, bot.ID); n != 0 {
		t.Fatal(n)
	}
}
func TestPGDeveloperMediaOwnershipReadinessAndAtomicRollback(t *testing.T) {
	f := newDeveloperMediaFixture(t, "")
	good := developerImage(t, f, f.actor, false, "image/png")
	foreign := developerImage(t, f, f.other, true, "image/png")
	bad := developerImage(t, f, f.actor, false, "application/octet-stream")
	if _, e := apppg.NewWithAuthority(f.db, f.guard).Apps(f.ctx, f.scope, &apps.AppPatch{PictureID: &good.ID}, "update"); !errors.Is(e, apps.ErrProvider) {
		t.Fatal("unconfigured media succeeded", e)
	}
	for _, tc := range []struct {
		id     string
		status int
	}{{foreign.ID, 403}, {bad.ID, 400}, {authorityUUID(t), 404}} {
		backendJSONCall(t, f.host, "PATCH", developerAppPath(f), f.token, map[string]any{"picture_id": good.ID, "background_id": tc.id, "name": "Must roll back"}, tc.status)
		if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_developer_media WHERE resource_id=$1`, f.app.ID); n != 0 {
			t.Fatal(n)
		}
	}
	query(t, f.db, `UPDATE owned_media_files SET state='deleting' WHERE id=$1`, bad.ID)
	backendJSONCall(t, f.host, "PATCH", developerAppPath(f), f.token, map[string]any{"picture_id": bad.ID}, 404)
	// Audit failure rolls back both the ordinary fields and both references.
	constraint := "synthetic_developer_media_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_apps_audit ADD CONSTRAINT `+constraint+` CHECK(app_id <> '`+f.app.ID+`'::uuid OR action<>'developers.apps.update') NOT VALID`)
	_, e := f.developerFixture.repo.Apps(f.ctx, f.scope, &apps.AppPatch{Name: botText("Must roll back"), PictureID: &good.ID}, "update")
	query(t, f.db, `ALTER TABLE owned_apps_audit DROP CONSTRAINT `+constraint)
	if e == nil {
		t.Fatal("audit rejection committed")
	}
	if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_developer_media WHERE resource_id=$1`, f.app.ID); n != 0 {
		t.Fatal(n)
	}
	a, e := f.developerFixture.repo.PublicApp(f.ctx, f.app.Slug)
	if e != nil || a.Name == "Must roll back" {
		t.Fatal(a, e)
	}
}
func TestPGDeveloperMediaCurrentAudienceAndNestedProjection(t *testing.T) {
	f := newDeveloperMediaFixture(t, "")
	file := developerImage(t, f, f.actor, false, "image/png")
	product := developerProduct(t, f, file.ID)
	backendCall(t, f.host, "GET", "/api/files/"+file.ID, "", "", 200)
	backendJSONCall(t, f.host, "PATCH", "/api/private/apps/"+f.app.ID+"/products/"+product.ID+"?dev="+f.dev+"&proj="+f.scope.Project, f.token, map[string]any{"state": map[string]bool{"is_enabled": false}}, 200)
	backendCall(t, f.host, "GET", "/api/files/"+file.ID, "", "", 404)
	backendCall(t, f.host, "GET", "/api/files/"+file.ID, f.otherToken, "", 404)
	// A current joined member may inspect unpublished/disabled managed resources.
	query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,0,now(),now(),now())`, authorityUUID(t), f.app.PublisherID, f.other)
	backendCall(t, f.host, "GET", "/api/files/"+file.ID, f.otherToken, "", 200)
	query(t, f.db, `UPDATE owned_publisher_members SET revoked_at=now() WHERE publisher_id=$1 AND account_id=$2`, f.app.PublisherID, f.other)
	backendCall(t, f.host, "GET", "/api/files/"+file.ID, f.otherToken, "", 404)
	backendJSONCall(t, f.host, "PATCH", developerAppPath(f), f.token, map[string]any{"picture_id": file.ID}, 200)
	for _, change := range []struct {
		hide, restore string
		arg           string
	}{
		{`UPDATE owned_custom_apps SET status=0 WHERE id=$1`, `UPDATE owned_custom_apps SET status=1 WHERE id=$1`, f.app.ID},
		{`UPDATE owned_publishers SET shadowbanned_at=now(),shadowban_reason='other' WHERE id=$1`, `UPDATE owned_publishers SET shadowbanned_at=NULL,shadowban_reason=NULL WHERE id=$1`, f.app.PublisherID},
		{`UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor},
	} {
		query(t, f.db, change.hide, change.arg)
		backendCall(t, f.host, "GET", "/api/files/"+file.ID, "", "", 404)
		query(t, f.db, change.restore, change.arg)
	}
	backendCall(t, f.host, "GET", "/api/files/"+file.ID, "", "", 200)
}
func TestPGDeveloperMediaReadWaitRechecksPrivacy(t *testing.T) {
	f := newDeveloperMediaFixture(t, "")
	file := developerImage(t, f, f.actor, false, "image/png")
	backendJSONCall(t, f.host, "PATCH", developerAppPath(f), f.token, map[string]any{"picture_id": file.ID}, 200)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		f.host.ServeHTTP(w, httptest.NewRequest("GET", "/api/files/"+file.ID, nil).WithContext(ctx))
		done <- w
	}()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	query(t, f.db, `UPDATE owned_custom_apps SET status=0 WHERE id=$1`, f.app.ID)
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if w := <-done; w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPGDeveloperMediaWriteWaitPreservesOriginalProof(t *testing.T) {
	f := newDeveloperMediaFixture(t, "")
	file := developerImage(t, f, f.actor, false, "image/png")
	cfg := f.config
	cfg.Now = time.Now
	cfg.AccessTTL, cfg.SessionTTL = 3*time.Second, 3*time.Second
	issuer, e := authority.New(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	pair := pgIssue(t, issuer, f.actor)
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(f.ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	repo := apppg.NewWithAuthority(f.db, guard).WithMedia(f.images)
	ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
	done := make(chan error, 1)
	go func() {
		_, e := repo.Apps(ctx, f.scope, &apps.AppPatch{PictureID: &file.ID, Name: botText("Expired change")}, "update")
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	// Preserve both original token and matching session. No session/history
	// mutation is attempted through another connection while locks are held.
	if remaining := time.Until(proof.Session.ExpiresAt.Add(20 * time.Millisecond)); remaining > 0 {
		time.Sleep(remaining)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, authority.ErrExpired) {
		t.Fatal(e)
	}
	if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_developer_media WHERE resource_id=$1`, f.app.ID); n != 0 {
		t.Fatal(n)
	}
}
func TestPGDeveloperQuotaConfiguredPolicyAndIsolation(t *testing.T) {
	f := newDeveloperMediaFixture(t, "")
	backendCall(t, f.host, "GET", "/api/develop/quota", "", "", 401)
	backendCall(t, f.host, "GET", "/api/develop/quota", f.token, "", 503)
	query(t, f.db, `INSERT INTO owned_developer_quota_accounts(account_id,publisher_id)VALUES($1,$2)`, f.actor, f.app.PublisherID)
	botCreate(t, f.botFixture, "quota")
	got := backendDecode[apps.DeveloperQuota](t, backendCall(t, f.host, "GET", "/api/develop/quota", f.token, "", 200))
	noStorage := backendApp(t, f.db, f.config)
	withoutStorage := backendDecode[apps.DeveloperQuota](t, backendCall(t, noStorage, "GET", "/api/develop/quota", f.token, "", 200))
	if withoutStorage != got {
		t.Fatal(withoutStorage, got)
	}
	if got.Total != 20 || got.Used != 1 {
		t.Fatal(got)
	}
	backendCall(t, f.host, "GET", "/api/develop/quota", f.otherToken, "", 503)
	backendCall(t, f.host, "GET", "/api/develop/quota?dev="+f.dev, f.token, "", 400)
	backendCall(t, f.host, "POST", "/api/develop/quota", f.token, "", 405)
	// Bindings do not authorize another account to read a publisher's capacity.
	query(t, f.db, `INSERT INTO owned_developer_quota_accounts(account_id,publisher_id)VALUES($1,$2)`, f.other, f.app.PublisherID)
	backendCall(t, f.host, "GET", "/api/develop/quota", f.otherToken, "", 403)
	query(t, f.db, `UPDATE owned_bot_quotas SET expires_at=clock_timestamp()-interval '1 second' WHERE publisher_id=$1`, f.app.PublisherID)
	backendCall(t, f.host, "GET", "/api/develop/quota", f.token, "", 503)
}
func TestPGDeveloperQuotaWaitUsesCurrentPolicyAndProof(t *testing.T) {
	t.Run("policy", func(t *testing.T) {
		f := newDeveloperMediaFixture(t, "")
		query(t, f.db, `INSERT INTO owned_developer_quota_accounts(account_id,publisher_id)VALUES($1,$2)`, f.actor, f.app.PublisherID)
		ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
		defer cancel()
		tx, e := f.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		var pid int
		if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_bot_quotas SET expires_at=clock_timestamp()-interval '1 second' WHERE publisher_id=$1`, f.app.PublisherID); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { _, e := f.repo.DeveloperQuota(ctx, f.actor); done <- e }()
		waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_bot_quotas")
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		if e = <-done; !errors.Is(e, apps.ErrUnavailable) {
			t.Fatal(e)
		}

	})

	t.Run("original_proof", func(t *testing.T) {
		f := newDeveloperMediaFixture(t, "")
		query(t, f.db, `INSERT INTO owned_developer_quota_accounts(account_id,publisher_id)VALUES($1,$2)`, f.actor, f.app.PublisherID)
		cfg := f.config
		cfg.Now = time.Now
		cfg.AccessTTL, cfg.SessionTTL = 3*time.Second, 3*time.Second
		issuer, e := authority.New(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		pair := pgIssue(t, issuer, f.actor)
		verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		proof, e := verifier.Authenticate(f.ctx, pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
		repo := botpg.NewWithAuthority(f.db, guard)
		ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 10*time.Second)
		defer cancel()
		blocker, e := f.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer blocker.Rollback()
		var pid int
		if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		if _, e = blocker.ExecContext(ctx, `SELECT publisher_id FROM owned_bot_quotas WHERE publisher_id=$1 FOR UPDATE`, f.app.PublisherID); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { _, e := repo.DeveloperQuota(ctx, f.actor); done <- e }()
		waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_bot_quotas")
		if remaining := time.Until(proof.Session.ExpiresAt.Add(20 * time.Millisecond)); remaining > 0 {
			time.Sleep(remaining)
		}
		if e = blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e = <-done; !errors.Is(e, authority.ErrExpired) {
			t.Fatal("expired original proof accepted", e)
		}
		if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_bots WHERE app_id=$1`, f.app.ID); n != 0 {
			t.Fatal("quota read created reservation", n)
		}
	})
}

func TestPGDeveloperMediaQuotaSavedRestartPersistence(t *testing.T) {
	type saved struct {
		Scope             apps.Scope
		App               apps.App
		Product           apps.Product
		Bot               bots.Bot
		File, Token, Root string
		Authority         restartAuthoritySnapshot
		Audit             int
	}
	const marker = "developer-media-quota.json"
	if fixtureRestartPhase(t) == "before" {
		root := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "developer-media-original-objects")
		f := newDeveloperMediaFixture(t, root)
		cfg := f.config
		cfg.AccessTTL, cfg.SessionTTL = time.Hour, time.Hour
		at, wall := cfg.Now(), time.Now()
		cfg.Now = func() time.Time { return at.Add(time.Since(wall)) }
		issuer, e := authority.New(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		pair := pgIssue(t, issuer, f.actor)
		f.config = cfg
		f.token = pair.Token
		f.v, e = authority.NewVerifier(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		proof, e := f.v.Authenticate(context.Background(), f.token)
		if e != nil {
			t.Fatal(e)
		}
		f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.v)
		f.ctx = f.guard.Bind(context.Background(), proof)
		f.repo = botpg.NewWithAuthority(f.db, f.guard).WithProfileMedia(f.images)
		f.developerFixture.repo = apppg.NewWithAuthority(f.db, f.guard).WithMedia(f.images)
		f.files, e = mediapg.New(f.db, f.store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Hour})
		if e != nil {
			t.Fatal(e)
		}
		visibility := int32(0)
		policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
		f.host, e = application.NewPostgres(f.db, nil, application.Config{Authority: cfg, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 1 << 20})
		if e != nil {
			t.Fatal(e)
		}
		file := developerImage(t, f, f.actor, false, "image/png")
		a := backendDecode[apps.App](t, backendJSONCall(t, f.host, "PATCH", developerAppPath(f), f.token, map[string]any{"picture_id": file.ID}, 200))
		product := developerProduct(t, f, file.ID)
		b, e := f.repo.Bots(f.ctx, f.scope, "", &bots.Patch{Name: botText("persist_" + strings.ReplaceAll(f.actor, "-", "")), Nick: botText("Persisted image bot"), Slug: botText("persist"), PictureID: &file.ID}, "create")
		if e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_developer_quota_accounts(account_id,publisher_id)VALUES($1,$2)`, f.actor, f.app.PublisherID)
		saveRestartFixture(t, marker, saved{Scope: f.scope, App: a, Product: product, Bot: b[0], File: file.ID, Token: pair.Token, Root: root, Authority: saveAuthoritySnapshot(t, cfg), Audit: commerceCount(t, f.db, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1`, f.app.ID)})
		return
	}
	db := database(t)
	var state saved
	loadRestartFixture(t, marker, &state)
	cfg := restoreAuthoritySnapshot(t, state.Authority)
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := v.Authenticate(context.Background(), state.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), v)
	ctx := guard.Bind(context.Background(), proof)
	images := developmediapg.New(db)
	appRepo := apppg.NewWithAuthority(db, guard).WithMedia(images)
	botRepo := botpg.NewWithAuthority(db, guard).WithProfileMedia(images)
	a, e := appRepo.Apps(ctx, state.Scope, nil, "read")
	if e != nil || len(a) != 1 || devJSON(t, a[0]) != devJSON(t, state.App) {
		t.Fatal("original app/media changed", a, e)
	}
	products, e := appRepo.Products(ctx, state.Scope, state.Product.ID, nil, "read")
	if e != nil || len(products) != 1 || devJSON(t, products[0]) != devJSON(t, state.Product) {
		t.Fatal("original product/media changed", products, e)
	}
	b, e := botRepo.Bots(ctx, state.Scope, state.Bot.ID, nil, "read")
	if e != nil || len(b) != 1 || devJSON(t, b[0]) != devJSON(t, state.Bot) {
		t.Fatal("original bot/media changed", b, e)
	}
	q, e := botRepo.DeveloperQuota(ctx, state.Scope.Actor)
	if e != nil || q.Total != 20 || q.Used != 1 {
		t.Fatal(q, e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1`, state.App.ID); n != state.Audit {
		t.Fatal("read mutated audit", n)
	}
	store, e := mediastore.NewFilesystem(state.Root, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	host, e := application.NewPostgres(db, nil, application.Config{Authority: cfg, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: store, MaxMediaBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	if w := backendCall(t, host, "GET", "/api/files/"+state.File, "", "", 200); !bytes.Equal(w.Body.Bytes(), profilePNG(t)) {
		t.Fatal("original bytes changed")
	}
	got := backendDecode[apps.DeveloperQuota](t, backendCall(t, host, "GET", "/api/develop/quota", state.Token, "", 200))
	if got != q {
		t.Fatal(got, q)
	}
}

func TestPGDeveloperMediaManagerOwnerAfterFileAndAuditWait(t *testing.T) {
	for _, at := range []string{"file", "audit"} {
		t.Run(at, func(t *testing.T) {
			f := newDeveloperMediaFixture(t, "")
			file := developerImage(t, f, f.other, false, "image/png")
			query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,now(),now(),now())`, authorityUUID(t), f.app.PublisherID, f.other)
			proof, e := f.v.Authenticate(f.ctx, f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.guard.Bind(context.Background(), proof), 10*time.Second)
			defer cancel()
			scope := f.scope
			scope.Actor = f.other
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table := "owned_media_files"
			blocker, pid := atomicBlocker(t, locks, table, file.ID)
			if at == "audit" {
				if e = blocker.Rollback(); e != nil {
					t.Fatal(e)
				}
				blocker, e = f.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer blocker.Rollback()
				if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				table = "owned_apps_audit"
				if _, e = blocker.ExecContext(ctx, `LOCK TABLE owned_apps_audit IN ACCESS EXCLUSIVE MODE`); e != nil {
					t.Fatal(e)
				}
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.developerFixture.repo.Apps(ctx, scope, &apps.AppPatch{PictureID: &file.ID, Name: botText("Must roll back")}, "update")
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, table)
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, apps.ErrForbidden) {
				t.Fatal(e)
			}
			if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_developer_media WHERE resource_id=$1`, f.app.ID); n != 0 {
				t.Fatal(n)
			}
		})
	}
}

func TestPGDeveloperBotMediaCreateRollbackAndQuotaExpiry(t *testing.T) {
	f := newDeveloperMediaFixture(t, "")
	foreign := developerImage(t, f, f.other, true, "image/png")
	patch := bots.Patch{Name: botText("rollback_" + strings.ReplaceAll(f.actor, "-", "")), Nick: botText("Must roll back"), Slug: botText("rollback"), PictureID: &foreign.ID}
	before := botCount(t, f.botFixture, `SELECT count(*) FROM owned_authority_accounts`)
	if _, e := f.repo.Bots(f.ctx, f.scope, "", &patch, "create"); !errors.Is(e, bots.ErrForbidden) {
		t.Fatal(e)
	}
	if after := botCount(t, f.botFixture, `SELECT count(*) FROM owned_authority_accounts`); after != before {
		t.Fatal("orphan bot account", before, after)
	}
	file := developerImage(t, f, f.actor, false, "image/png")
	patch.PictureID = &file.ID
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
	// The policy will expire while the canonical file row keeps creation waiting.
	var expiry time.Time
	if e := f.db.QueryRowContext(ctx, `UPDATE owned_bot_quotas SET expires_at=clock_timestamp()+interval '1 second' WHERE publisher_id=$1 RETURNING expires_at`, f.app.PublisherID).Scan(&expiry); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.repo.Bots(ctx, f.scope, "", &patch, "create"); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	// Wait against server time without altering the original proof or policy.
	for {
		var expired bool
		if e := f.db.QueryRowContext(ctx, `SELECT clock_timestamp()>$1`, expiry).Scan(&expired); e != nil {
			t.Fatal(e)
		}
		if expired {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, bots.ErrQuota) {
		t.Fatal(e)
	}
	if after := botCount(t, f.botFixture, `SELECT count(*) FROM owned_authority_accounts`); after != before {
		t.Fatal("expired admission orphaned account", after)
	}
	if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_bots WHERE app_id=$1`, f.app.ID); n != 0 {
		t.Fatal(n)
	}
}

// A resource manager may retain an image uploaded earlier by another manager.
// Revoking that uploader while audit is blocked must affect the returned
// projection as well as the next metadata/byte read.
func TestPGDeveloperMediaAuditWaitUsesCurrentUploader(t *testing.T) {
	for _, kind := range []string{"app", "product", "bot"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeveloperMediaFixture(t, "")
			file := developerImage(t, f, f.other, false, "image/png")
			query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,now(),now(),now())`, authorityUUID(t), f.app.PublisherID, f.other)
			proof, e := f.v.Authenticate(f.ctx, f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			otherScope := f.scope
			otherScope.Actor = f.other
			otherContext := f.guard.Bind(context.Background(), proof)
			var mutate func(context.Context) (map[string]any, error)
			table := "owned_apps_audit"
			switch kind {
			case "app":
				if _, e = f.developerFixture.repo.Apps(otherContext, otherScope, &apps.AppPatch{PictureID: &file.ID}, "update"); e != nil {
					t.Fatal(e)
				}
				mutate = func(ctx context.Context) (map[string]any, error) {
					v, err := f.developerFixture.repo.Apps(ctx, f.scope, &apps.AppPatch{Name: botText("Current image audience")}, "update")
					if err != nil {
						return nil, err
					}
					return v[0].Picture, nil
				}
			case "product":
				v, err := f.developerFixture.repo.Products(otherContext, otherScope, "", &apps.ProductPatch{Identifier: botText("retained-image"), DisplayName: botText("Retained image"), Currency: botText("usd"), PictureID: &file.ID}, "create")
				if err != nil {
					t.Fatal(err)
				}
				mutate = func(ctx context.Context) (map[string]any, error) {
					out, err := f.developerFixture.repo.Products(ctx, f.scope, v[0].ID, &apps.ProductPatch{DisplayName: botText("Current image audience")}, "update")
					if err != nil {
						return nil, err
					}
					return out[0].Picture, nil
				}
			case "bot":
				b := botCreate(t, f.botFixture, "retained")
				if _, e = f.repo.Bots(otherContext, otherScope, b.ID, &bots.Patch{PictureID: &file.ID}, "update"); e != nil {
					t.Fatal(e)
				}
				table = "owned_bot_audit"
				mutate = func(ctx context.Context) (map[string]any, error) {
					v, err := f.repo.Bots(ctx, f.scope, b.ID, &bots.Patch{Nick: botText("Current image audience")}, "update")
					if err != nil {
						return nil, err
					}
					return v[0].Account.Profile.Picture, nil
				}
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			blocker, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.ExecContext(ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`); e != nil {
				t.Fatal(e)
			}
			type result struct {
				picture map[string]any
				err     error
			}
			done := make(chan result, 1)
			go func() { picture, err := mutate(ctx); done <- result{picture, err} }()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, table)
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if got.err != nil || got.picture != nil {
				t.Fatal("stale uploader metadata after audit wait", got.picture, got.err)
			}
			backendCall(t, f.host, "GET", "/api/files/"+file.ID, "", "", 404)
		})
	}
}

func TestPGDeveloperBotUpdateWaitUsesCurrentPunishment(t *testing.T) {
	for _, at := range []string{"file", "audit"} {
		t.Run(at, func(t *testing.T) {
			f := newDeveloperMediaFixture(t, "")
			b := botCreate(t, f.botFixture, "punishment")
			file := developerImage(t, f, f.actor, false, "image/png")
			beforeVersion := botCount(t, f.botFixture, `SELECT version FROM owned_authority_accounts WHERE id=$1`, b.Account.ID)
			beforeAudit := botCount(t, f.botFixture, `SELECT count(*) FROM owned_bot_audit WHERE app_id=$1`, f.app.ID)
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
			table := "owned_media_files"
			if at == "audit" {
				if e := blocker.Rollback(); e != nil {
					t.Fatal(e)
				}
				var e error
				blocker, e = f.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer blocker.Rollback()
				if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				table = "owned_bot_audit"
				if _, e = blocker.ExecContext(ctx, `LOCK TABLE owned_bot_audit IN ACCESS EXCLUSIVE MODE`); e != nil {
					t.Fatal(e)
				}
			}
			var starts time.Time
			if e := f.db.QueryRowContext(ctx, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()+interval '3 seconds')RETURNING starts_at`, b.Account.ID).Scan(&starts); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.repo.Bots(ctx, f.scope, b.ID, &bots.Patch{PictureID: &file.ID, Nick: botText("Must roll back")}, "update")
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, table)
			// No policy or account rewrite while locks are held: the originally
			// scheduled punishment becomes active under the database wall clock.
			for {
				var active bool
				if e := f.db.QueryRowContext(ctx, `SELECT clock_timestamp()>$1`, starts).Scan(&active); e != nil {
					t.Fatal(e)
				}
				if active {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, bots.ErrForbidden) {
				t.Fatal("punished update committed", e)
			}
			if n := botCount(t, f.botFixture, `SELECT version FROM owned_authority_accounts WHERE id=$1`, b.Account.ID); n != beforeVersion {
				t.Fatal("account update survived rollback", n)
			}
			if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_bot_audit WHERE app_id=$1`, f.app.ID); n != beforeAudit {
				t.Fatal("audit survived rollback", n)
			}
			if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_developer_media WHERE kind='bot' AND resource_id=$1`, b.ID); n != 0 {
				t.Fatal("media survived rollback", n)
			}
			out, e := f.repo.Bots(f.ctx, f.scope, b.ID, nil, "read")
			if e != nil || len(out) != 1 || out[0].Account.Nick != b.Account.Nick {
				t.Fatal("bot profile survived rollback", out, e)
			}
		})
	}
}
