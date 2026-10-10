// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/media"
	"strings"
	"sync"
	"testing"
	"time"
)

type botFixture struct {
	developerFixture
	repo  *botpg.Repository
	scope apps.Scope
	ctx   context.Context
	app   apps.App
	query string
	http  http.Handler
}

func newBotFixture(t *testing.T) botFixture {
	t.Helper()
	base := newDeveloperFixture(t)
	project, app, q := base.setup(t)
	backendCall(t, base.handler, "PATCH", "/api/private/apps/"+app.ID+q, base.token, `{"status":1}`, 200)
	query(t, base.db, botpg.Schema)
	query(t, base.db, `INSERT INTO owned_bot_quotas(publisher_id,bots_total,miniapps_total,starts_at)VALUES($1,20,20,now())`, app.PublisherID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	p, e := base.v.Authenticate(ctx, base.token)
	if e != nil {
		t.Fatal(e)
	}
	r := botpg.NewWithAuthority(base.db, base.guard)
	if e = r.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	f := botFixture{developerFixture: base, repo: r, scope: apps.Scope{Actor: base.actor, Developer: base.dev, Project: project.ID, App: app.ID}, ctx: base.guard.Bind(ctx, p), app: app, query: q + "&app=" + app.ID}
	f.http = (&bots.Service{Repository: r, Authority: base.v, Bind: base.guard.Bind}).Handler()
	return f
}
func botText(s string) *string { return &s }
func botCreate(t *testing.T, f botFixture, suffix string) bots.Bot {
	t.Helper()
	name := "synthetic_bot_" + strings.ReplaceAll(f.actor, "-", "") + suffix
	v, e := f.repo.Bots(f.ctx, f.scope, "", &bots.Patch{Name: &name, Nick: botText("Synthetic bot"), Slug: botText("bot" + suffix), Bio: botText("Synthetic profile")}, "create")
	if e != nil {
		t.Fatal(e)
	}
	return v[0]
}
func botCount(t *testing.T, f botFixture, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if e := f.db.QueryRowContext(f.ctx, q, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func botManifest(id string) *bots.MiniManifest {
	return &bots.MiniManifest{ID: "test.synthetic." + strings.ReplaceAll(id, "-", ""), Name: "Synthetic miniapp", Version: "1.0.0", Entry: "main.js", Permissions: []string{}}
}
func botMini(t *testing.T, f botFixture, suffix string) bots.MiniApp {
	t.Helper()
	stage := 2
	v, e := f.repo.MiniApps(f.ctx, f.scope, "", &bots.MiniPatch{Slug: botText("synthetic-" + strings.ReplaceAll(f.actor, "-", "") + suffix), Stage: &stage, Manifest: botManifest(authorityUUID(t))}, "create")
	if e != nil {
		t.Fatal(e)
	}
	return v[0]
}
func TestPGBotsCanonicalLifecycleAndCommands(t *testing.T) {
	f := newBotFixture(t)
	b := botCreate(t, f, "one")
	if b.ID == b.Account.ID || b.Account.ID == f.actor || b.Account.AutomatedID != b.ID || b.Account.Profile.ID == "" {
		t.Fatal("bot identity was conflated", b)
	}
	if n := botCount(t, f, `SELECT (SELECT count(*) FROM owned_authority_sessions WHERE account_id=$1)+(SELECT count(*)FROM owned_authority_grants WHERE account_id=$1)`, b.Account.ID); n != 0 {
		t.Fatal("implicit credentials/grants", n)
	}
	public, e := f.repo.PublicBot(context.Background(), b.ID)
	if e != nil || public.Developer == nil || public.Developer.PublisherID != f.app.PublisherID {
		t.Fatal(public, e)
	}
	commands := []develop.BotCommand{{Name: "ping", Description: "Synthetic command", Parameters: []develop.BotCommandParameter{{Name: "value", Required: true}}}}
	cfg, e := f.repo.Chat(f.ctx, f.scope, b.ID, &bots.ManifestPatch{Commands: &commands}, false)
	if e != nil || len(cfg.Commands) != 1 {
		t.Fatal(cfg, e)
	}
	other := develop.BotCommand{Name: "other", Description: "Synthetic second command"}
	if _, e = f.repo.Command(f.ctx, f.scope, b.ID, "", &other, "create"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Command(f.ctx, f.scope, b.ID, "", &other, "create"); !errors.Is(e, bots.ErrConflict) {
		t.Fatal("duplicate command", e)
	}
	other.Description = "Synthetic changed description"
	if _, e = f.repo.Command(f.ctx, f.scope, b.ID, "other", &other, "update"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Command(f.ctx, f.scope, b.ID, "ping", nil, "delete"); e != nil {
		t.Fatal(e)
	}
	v, e := f.repo.PublicChat(context.Background(), b.ID)
	if e != nil || len(v.Commands) != 1 || v.Commands[0].Description != other.Description {
		t.Fatal(v, e)
	}
	inactive := false
	if _, e = f.repo.Bots(f.ctx, f.scope, b.ID, &bots.Patch{IsActive: &inactive}, "update"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.PublicBot(context.Background(), b.ID); !errors.Is(e, bots.ErrNotFound) {
		t.Fatal("inactive bot exposed", e)
	}
	active := true
	if _, e = f.repo.Bots(f.ctx, f.scope, b.ID, &bots.Patch{IsActive: &active}, "update"); e != nil {
		t.Fatal(e)
	}
	before := botCount(t, f, `SELECT version FROM owned_authority_accounts WHERE id=$1`, b.Account.ID)
	if _, e = f.repo.Bots(f.ctx, f.scope, b.ID, nil, "delete"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Bots(f.ctx, f.scope, b.ID, nil, "delete"); e != nil {
		t.Fatal("repeat deletion", e)
	}
	if n := botCount(t, f, `SELECT version FROM owned_authority_accounts WHERE id=$1 AND disabled`, b.Account.ID); n != before+1 {
		t.Fatal("canonical account not atomically disabled/versioned", n, before)
	}
	if _, e = f.repo.PublicChat(context.Background(), b.ID); !errors.Is(e, bots.ErrNotFound) {
		t.Fatal("deleted config exposed", e)
	}
}
func TestPGBotsScopeIsolationHTTPAndFreshAuthority(t *testing.T) {
	f := newBotFixture(t)
	b := botCreate(t, f, "scope")
	root := "/api/private/bots/" + b.ID
	backendCall(t, f.http, "GET", root+f.query, "", "", 401)
	backendCall(t, f.http, "GET", root+f.query, f.otherToken, "", 403)
	backendCall(t, f.http, "GET", "/api/bots/"+b.ID+"/commands", "", "", 200)
	backendCall(t, f.http, "POST", root+"/commands"+f.query, f.token, `{"name":"hello","description":"Synthetic help","parameters":[]}`, 200)
	backendCall(t, f.http, "POST", root+"/commands"+f.query, f.token, `{"name":"run","description":"Synthetic","execute":"not permitted"}`, 400)
	backendCall(t, f.http, "POST", root+"/keys"+f.query, f.token, `{"label":"Synthetic"}`, 200)
	wrong := f.scope
	wrong.App = authorityUUID(t)
	if _, e := f.repo.Bots(f.ctx, wrong, b.ID, nil, "read"); !errors.Is(e, bots.ErrNotFound) {
		t.Fatal("app substitution", e)
	}
	if _, e := f.repo.Bots(context.Background(), f.scope, b.ID, nil, "read"); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unbound proof", e)
	}
	if _, e := botpg.New(f.db).Bots(f.ctx, f.scope, b.ID, nil, "read"); !errors.Is(e, bots.ErrUnavailable) {
		t.Fatal("unguarded constructor", e)
	}
	if _, e := botpg.NewWithAuthority(database(t), f.guard).Bots(f.ctx, f.scope, b.ID, nil, "read"); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("wrong pool", e)
	}
	query(t, f.db, `UPDATE owned_custom_apps SET status=2 WHERE id=$1`, f.app.ID)
	backendCall(t, f.http, "GET", "/api/bots/"+b.ID, "", "", 404)
	if _, e := f.repo.Command(f.ctx, f.scope, b.ID, "", &develop.BotCommand{Name: "denied", Description: "Synthetic"}, "create"); !errors.Is(e, bots.ErrNotFound) {
		t.Fatal("suspended app write", e)
	}
}
func TestPGBotsQuotaConcurrencyAndAtomicRollback(t *testing.T) {
	f := newBotFixture(t)
	query(t, f.db, `UPDATE owned_bot_quotas SET bots_total=1 WHERE publisher_id=$1`, f.app.PublisherID)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "synthetic_bot_race_" + strings.ReplaceAll(f.actor, "-", "") + fmt.Sprint(i)
			_, e := f.repo.Bots(f.ctx, f.scope, "", &bots.Patch{Name: &name, Nick: botText("Synthetic race"), Slug: botText(fmt.Sprint("race", i))}, "create")
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	ok, denied := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, bots.ErrQuota) {
			denied++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || denied != 7 {
		t.Fatal(ok, denied)
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_bots WHERE app_id=$1`, f.app.ID); n != 1 {
		t.Fatal("quota overshoot", n)
	}
	query(t, f.db, `UPDATE owned_bot_quotas SET bots_total=2 WHERE publisher_id=$1`, f.app.PublisherID)
	constraint := "synthetic_bot_fail_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_bot_audit ADD CONSTRAINT `+constraint+` CHECK(actor_id<>'`+f.actor+`'::uuid)NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_bot_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	name := "synthetic_rollback_" + strings.ReplaceAll(f.actor, "-", "")
	_, e := f.repo.Bots(f.ctx, f.scope, "", &bots.Patch{Name: &name, Nick: botText("Synthetic rollback"), Slug: botText("rollback")}, "create")
	if e == nil {
		t.Fatal("injected failure committed")
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_authority_accounts WHERE name=$1`, name); n != 0 {
		t.Fatal("orphan canonical account", n)
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_bots WHERE app_id=$1`, f.app.ID); n != 1 {
		t.Fatal("partial bot commit", n)
	}
}
func TestPGBotsOpposingEditorsAndOwnerDisable(t *testing.T) {
	a, b := newBotFixture(t), newBotFixture(t)
	botA, botB := botCreate(t, a, "a"), botCreate(t, b, "b")
	for _, v := range []struct {
		f     botFixture
		other string
	}{{a, b.actor}, {b, a.actor}} {
		query(t, v.f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,now(),now(),now())`, authorityUUID(t), v.f.app.PublisherID, v.other)
	}
	scopeA := b.scope
	scopeA.Actor = a.actor
	scopeB := a.scope
	scopeB.Actor = b.actor
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, v := range []struct {
		f   botFixture
		s   apps.Scope
		bot string
	}{{a, scopeA, botB.ID}, {b, scopeB, botA.ID}} {
		go func(v struct {
			f   botFixture
			s   apps.Scope
			bot string
		}) {
			<-start
			for i := 0; i < 4; i++ {
				if _, e := v.f.repo.Bots(v.f.ctx, v.s, v.bot, &bots.Patch{Nick: botText(fmt.Sprint("Synthetic opposite ", i))}, "update"); e != nil {
					errs <- e
					return
				}
			}
			errs <- nil
		}(v)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal("opposing account order failed", e)
		}
	}
	base := atomicAuthorityFixture{db: a.db, ctx: a.ctx, actor: b.actor}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", b.actor)
	if _, e := blocker.ExecContext(a.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, b.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := a.repo.Bots(a.ctx, scopeA, botB.ID, &bots.Patch{Nick: botText("Must not commit")}, "update")
		done <- e
	}()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, bots.ErrForbidden) {
		t.Fatal("owner disabled during lock wait", e)
	}
	var nick string
	if e := a.db.QueryRow(`SELECT nick FROM owned_authority_accounts WHERE id=$1`, botB.Account.ID).Scan(&nick); e != nil || nick == "Must not commit" {
		t.Fatal(nick, e)
	}
}
func TestPGBotsRevocationAndControlledDeactivation(t *testing.T) {
	f := newBotFixture(t)
	b := botCreate(t, f, "revoke")
	off, on := false, true
	if _, e := f.repo.Bots(f.ctx, f.scope, b.ID, &bots.Patch{IsActive: &off}, "update"); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, b.Account.ID)
	if _, e := f.repo.Bots(f.ctx, f.scope, b.ID, &bots.Patch{IsActive: &on}, "update"); !errors.Is(e, bots.ErrForbidden) {
		t.Fatal("developer overrode later canonical disable", e)
	}
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.actor}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE account_id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.repo.Bots(f.ctx, f.scope, b.ID, nil, "delete"); done <- e }()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked waiter deleted bot", e)
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_bots WHERE id=$1 AND deleted_at IS NULL`, b.ID); n != 1 {
		t.Fatal("revoked mutation effect", n)
	}
}
func TestPGBotsPolicyTimeBoundariesAndPublisherRevocation(t *testing.T) {
	f := newBotFixture(t)
	b := botCreate(t, f, "policy")
	var now time.Time
	if e := f.db.QueryRowContext(f.ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		t.Fatal(e)
	}
	before, later := now.Add(-time.Hour).Truncate(time.Microsecond), now.Add(time.Hour).Truncate(time.Microsecond)
	command := develop.BotCommand{Name: "policy", Description: "Synthetic policy command"}
	for _, p := range []authority.Punishment{{Kind: "disable_account", StartsAt: &later}, {Kind: "disable_account", ExpiresAt: &before}} {
		if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, b.Account.ID, nil, []authority.Punishment{p}); e != nil {
			t.Fatal(e)
		}
		if _, e := f.repo.Bots(f.ctx, f.scope, b.ID, &bots.Patch{Nick: botText("Synthetic allowed")}, "update"); e != nil {
			t.Fatal("future/expired punishment blocked update", e)
		}
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, b.Account.ID, nil, []authority.Punishment{{Kind: "disable_account", StartsAt: &before, ExpiresAt: &later}}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.Command(f.ctx, f.scope, b.ID, "", &command, "create"); !errors.Is(e, bots.ErrForbidden) {
		t.Fatal("active target punishment ignored", e)
	}
	if _, e := f.repo.PublicBot(context.Background(), b.ID); !errors.Is(e, bots.ErrNotFound) {
		t.Fatal("active target punishment exposed", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, b.Account.ID, nil, nil); e != nil {
		t.Fatal(e)
	}
	name := "synthetic_policy_" + strings.ReplaceAll(f.actor, "-", "")
	p := &bots.Patch{Name: &name, Nick: botText("Synthetic capacity"), Slug: botText("capacity")}
	query(t, f.db, `UPDATE owned_bot_quotas SET expires_at=$2 WHERE publisher_id=$1`, f.app.PublisherID, before)
	if _, e := f.repo.Bots(f.ctx, f.scope, "", p, "create"); !errors.Is(e, bots.ErrQuota) {
		t.Fatal("expired quota used", e)
	}
	query(t, f.db, `UPDATE owned_bot_quotas SET starts_at=$2,expires_at=NULL WHERE publisher_id=$1`, f.app.PublisherID, later)
	if _, e := f.repo.Bots(f.ctx, f.scope, "", p, "create"); !errors.Is(e, bots.ErrQuota) {
		t.Fatal("future quota used", e)
	}
	query(t, f.db, `UPDATE owned_bot_quotas SET starts_at=$2 WHERE publisher_id=$1`, f.app.PublisherID, before)
	query(t, f.db, `UPDATE owned_publisher_members SET revoked_at=$3 WHERE publisher_id=$1 AND account_id=$2`, f.app.PublisherID, f.actor, before)
	if _, e := f.repo.Command(f.ctx, f.scope, b.ID, "", &command, "create"); !errors.Is(e, bots.ErrForbidden) {
		t.Fatal("revoked publisher member wrote command", e)
	}
	if n := botCount(t, f, `SELECT count(*) FROM owned_bot_chat WHERE bot_id=$1`, b.ID); n != 0 {
		t.Fatal("denied policy checks left metadata", n)
	}
}

func botZip(t *testing.T, id string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	m, _ := w.Create("manifest.json")
	raw, _ := json.Marshal(botManifest(id))
	_, _ = m.Write(raw)
	entry, _ := w.Create("main.js")
	_, _ = entry.Write([]byte("// Synthetic bytes only; never executed."))
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func botMedia(t *testing.T, f botFixture, root string) *mediapg.Repository {
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
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1,10485760)ON CONFLICT(account_id)DO NOTHING`, id)
	}
	f.repo.WithMedia(m)
	return m
}
func botUpload(t *testing.T, f botFixture, m *mediapg.Repository, data []byte, key string, public bool, actor string) media.File {
	t.Helper()
	token := f.token
	if actor == f.other {
		token = f.otherToken
	}
	p, e := f.v.Authenticate(f.ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := f.guard.Bind(f.ctx, p)
	v, _, e := m.Upload(ctx, actor, media.Upload{Name: "synthetic.zip", MIMEType: "application/zip", Size: int64(len(data)), SHA256: mediaDigest(string(data)), IdempotencyKey: key, Public: public}, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGBotsMiniappCanonicalPackagesAndPrivacy(t *testing.T) {
	f := newBotFixture(t)
	m := botMini(t, f, "mini")
	mediaRepo := botMedia(t, f, t.TempDir())
	data := botZip(t, authorityUUID(t))
	file := botUpload(t, f, mediaRepo, data, "public-package", true, f.actor)
	private := botUpload(t, f, mediaRepo, data, "private-package", false, f.actor)
	foreign := botUpload(t, f, mediaRepo, data, "foreign-package", true, f.other)
	bad := botUpload(t, f, mediaRepo, []byte("not a valid ZIP"), "malformed-package", true, f.actor)
	for _, v := range []struct {
		id   string
		want error
	}{{private.ID, bots.ErrNotFound}, {foreign.ID, bots.ErrForbidden}, {bad.ID, bots.ErrInvalid}} {
		if _, e := f.repo.LinkPackage(f.ctx, f.scope, m.ID, v.id); !errors.Is(e, v.want) {
			t.Fatal("invalid package link", e, v.want)
		}
	}
	linked, e := f.repo.LinkPackage(f.ctx, f.scope, m.ID, file.ID)
	if e != nil || linked.Size != file.Size || linked.SHA256 != file.SHA256 {
		t.Fatal(linked, e)
	}
	public, e := f.repo.PublicMiniApp(context.Background(), m.Slug)
	if e != nil || public.PackageSHA256 == nil || *public.PackageSHA256 != file.SHA256 || public.Author == nil || *public.Author != f.dev {
		t.Fatal(public, e)
	}
	w := backendCall(t, f.http, "GET", "/api/miniapps/"+m.Slug+"/package", "", "", 200)
	if !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("wrong package bytes")
	}
	if n := botCount(t, f, `SELECT download_count FROM owned_bot_miniapps WHERE id=$1`, m.ID); n != 1 {
		t.Fatal(n)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, body, e := f.repo.OpenPackage(context.Background(), m.Slug)
			if e == nil {
				_, e = io.Copy(io.Discard, body)
				body.Close()
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("concurrent download counter", e)
		}
	}
	if n := botCount(t, f, `SELECT download_count FROM owned_bot_miniapps WHERE id=$1`, m.ID); n != 5 {
		t.Fatal(n)
	}
	query(t, f.db, `UPDATE owned_media_files SET is_public=false WHERE id=$1`, file.ID)
	if _, _, e = f.repo.OpenPackage(context.Background(), m.Slug); !errors.Is(e, bots.ErrNotFound) {
		t.Fatal("private media leaked", e)
	}
	public, e = f.repo.PublicMiniApp(context.Background(), m.Slug)
	if e != nil || public.PackageURL != nil {
		t.Fatal("private media metadata leaked", public, e)
	}
	query(t, f.db, `UPDATE owned_media_files SET is_public=true WHERE id=$1`, file.ID)
	query(t, f.db, `UPDATE owned_custom_apps SET status=2 WHERE id=$1`, f.app.ID)
	if _, _, e = f.repo.OpenPackage(context.Background(), m.Slug); !errors.Is(e, bots.ErrNotFound) {
		t.Fatal("suspended miniapp bytes served", e)
	}
}
func TestPGBotsRestartPersistence(t *testing.T) {
	db := database(t)
	query(t, db, botpg.Schema)
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	statePath := filepath.Join(root, "synthetic-bots-restart.json")
	type state struct{ Bot, Account, Mini, File, Hash string }
	var saved state
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		f := newBotFixture(t)
		b := botCreate(t, f, "restart")
		commands := []develop.BotCommand{{Name: "persisted", Description: "Synthetic restart command"}}
		if _, e := f.repo.Chat(f.ctx, f.scope, b.ID, &bots.ManifestPatch{Commands: &commands}, false); e != nil {
			t.Fatal(e)
		}
		mini := botMini(t, f, "restart")
		m := botMedia(t, f, filepath.Join(root, "synthetic-bot-package-store"))
		file := botUpload(t, f, m, botZip(t, authorityUUID(t)), "restart-package", true, f.actor)
		if _, e := f.repo.LinkPackage(f.ctx, f.scope, mini.ID, file.ID); e != nil {
			t.Fatal(e)
		}
		saved = state{b.ID, b.Account.ID, mini.Slug, file.ID, file.SHA256}
		raw, _ := json.Marshal(saved)
		if e := os.WriteFile(statePath, raw, 0600); e != nil {
			t.Fatal(e)
		}
	case "after":
		raw, e := os.ReadFile(statePath)
		if e != nil || json.Unmarshal(raw, &saved) != nil {
			t.Fatal("missing restart state", e)
		}
	default:
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), nil)
	store, e := mediastore.NewFilesystem(filepath.Join(root, "synthetic-bot-package-store"), 6<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	m, e := mediapg.New(db, store, guard, mediapg.Config{MaxFileBytes: 5 << 20, PendingTTL: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	r := botpg.New(db).WithMedia(m)
	b, e := r.PublicBot(context.Background(), saved.Bot)
	if e != nil || b.Account.ID != saved.Account {
		t.Fatal("bot identity lost", b, e)
	}
	cfg, e := r.PublicChat(context.Background(), saved.Bot)
	if e != nil || len(cfg.Commands) != 1 || cfg.Commands[0].Name != "persisted" {
		t.Fatal("commands lost", cfg, e)
	}
	file, reader, e := r.OpenPackage(context.Background(), saved.Mini)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(reader)
	reader.Close()
	if e != nil || file.ID != saved.File || file.SHA256 != saved.Hash || mediaDigest(string(raw)) != saved.Hash {
		t.Fatal("package content lost", e)
	}
}
