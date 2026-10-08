// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/passkitpg"
	"solsynth.local/go-migration/internal/testpasskit"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/passkit"
)

type passkitFixture struct {
	mediaFixture
	signing testpasskit.Fixture
	passes  *passkitpg.Repository
	app     *application.Backend
}

func newPasskitFixture(t *testing.T) passkitFixture {
	t.Helper()
	f := passkitFixture{mediaFixture: newMediaFixture(t), signing: testpasskit.New(t)}
	for _, actor := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), actor, []authority.Rule{{Pattern: "media.files.*"}, {Pattern: passkit.ProvisionPermission}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	generator, e := passkit.NewGenerator(f.signing.Config)
	if e != nil {
		t.Fatal(e)
	}
	f.passes, e = passkitpg.New(f.db, f.guard, passkitpg.Config{Generator: generator, Media: f.store})
	if e != nil {
		t.Fatal(e)
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	f.app, e = application.NewPostgres(f.db, nil, application.Config{Authority: f.config, SessionService: f.issuer, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 1 << 20, AppleWallet: &f.signing.Config})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return f
}
func passFiles(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		t.Fatal(e)
	}
	out := map[string][]byte{}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		out[f.Name] = b
	}
	if len(out["signature"]) == 0 {
		t.Fatal("unsigned pass")
	}
	return out
}
func passJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if e := json.Unmarshal(passFiles(t, b)["pass.json"], &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func passCall(t *testing.T, h http.Handler, method, path, authorization, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatal(method, path, w.Code, w.Body.String())
	}
	return w
}
func passToken(t *testing.T, b []byte) string {
	t.Helper()
	token, ok := passJSON(t, b)["authenticationToken"].(string)
	if !ok || len(token) != 48 {
		t.Fatal("invalid pass token")
	}
	return token
}
func passType(f passkitFixture) string { typ, _ := f.signing.Config.Signer.Identity(); return typ }
func passRegisterPath(f passkitFixture, serial string) string {
	return "/api/passkit/v1/devices/synthetic-device/registrations/" + passType(f) + "/" + serial
}

func TestPGPasskitCompositionRegistrationIsolationAndUpdates(t *testing.T) {
	f := newPasskitFixture(t)
	download := passCall(t, f.app, "GET", "/api/accounts/me/passbook/member", "Bearer "+f.token, "", 200)
	token := passToken(t, download.Body.Bytes())
	auth := "ApplePass " + token
	passCall(t, f.app, "GET", "/api/accounts/me/passbook/member", "", "", 401)
	passCall(t, f.app, "GET", "/api/accounts/me/passbook/member", auth, "", 401)
	path := passRegisterPath(f, f.actor)
	passCall(t, f.app, "POST", path, auth, `{"pushToken":"synthetic-push-1"}`, 201)
	passCall(t, f.app, "POST", path, auth, `{"push_token":"synthetic-push-2"}`, 200)
	passCall(t, f.app, "POST", path, auth, `{"pushToken":"first","pushToken":"second"}`, 400)
	passCall(t, f.app, "POST", path, "Bearer "+f.token, `{"pushToken":"wrong-scheme"}`, 401)
	var push string
	if e := f.db.QueryRow(`SELECT push_token FROM owned_apple_pass_registrations WHERE pass_type=$1::text AND serial_number=$2::uuid`, passType(f), f.actor).Scan(&push); e != nil || push != "synthetic-push-2" {
		t.Fatal(push, e)
	}
	other := passCall(t, f.app, "GET", "/stargate/accounts/me/passbook/member", "Bearer "+f.otherToken, "", 200)
	otherToken := passToken(t, other.Body.Bytes())
	passCall(t, f.app, "POST", passRegisterPath(f, f.other), "ApplePass "+otherToken, `{"pushToken":"synthetic-other-push"}`, 201)
	updates := "/api/passkit/v1/devices/synthetic-device/registrations/" + passType(f)
	deviceList := passCall(t, f.app, "GET", updates, "", "", 200)
	var deviceSerials passkit.Serials
	if e := json.Unmarshal(deviceList.Body.Bytes(), &deviceSerials); e != nil || len(deviceSerials.SerialNumbers) != 2 {
		t.Fatal("device-only protocol list", deviceSerials, e)
	}
	passCall(t, f.app, "GET", strings.Replace(updates, "synthetic-device", "unknown-synthetic-device", 1), "", "", 204)
	passCall(t, f.app, "GET", updates, "Bearer "+f.token, "", 401)
	w := passCall(t, f.app, "GET", updates, auth, "", 200)
	var serials passkit.Serials
	if e := json.Unmarshal(w.Body.Bytes(), &serials); e != nil || len(serials.SerialNumbers) != 1 || serials.SerialNumbers[0] != f.actor {
		t.Fatal("cross-account serial leak", serials, e)
	}
	passCall(t, f.app, "GET", updates+"?passesUpdatedSince="+serials.LastUpdated, auth, "", 204)
	passCall(t, f.app, "GET", updates+"?passesUpdatedSince=9223372036854775807", "ApplePass "+strings.Repeat("0", 48), "", 401)
	latest := "/passport/passkit/v1/passes/" + passType(f) + "/" + f.actor
	passCall(t, f.app, "GET", latest, "ApplePass "+otherToken, "", 401)
	old, _ := strconv.ParseInt(serials.LastUpdated, 10, 64)
	query(t, f.db, `UPDATE owned_authority_accounts SET nick='Changed Synthetic Member' WHERE id=$1::uuid`, f.actor)
	w = passCall(t, f.app, "GET", updates+"?passesUpdatedSince="+serials.LastUpdated, auth, "", 200)
	if e := json.Unmarshal(w.Body.Bytes(), &serials); e != nil {
		t.Fatal(e)
	}
	next, e := strconv.ParseInt(serials.LastUpdated, 10, 64)
	if e != nil || next <= old {
		t.Fatal("nonmonotonic update", old, next, e)
	}
	w = passCall(t, f.app, "GET", latest, auth, "", 200)
	if !bytes.Contains(passFiles(t, w.Body.Bytes())["pass.json"], []byte("Changed Synthetic Member")) || w.Header().Get("ETag") != `W/"`+serials.LastUpdated+`"` {
		t.Fatal("latest projection not updated")
	}
	r := httptest.NewRequest("GET", latest, nil)
	r.Header.Set("Authorization", auth)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	f.app.ServeHTTP(cached, r)
	if cached.Code != 304 || cached.Body.Len() != 0 {
		t.Fatal(cached.Code)
	}
	passCall(t, f.app, "DELETE", path, auth, "", 200)
	passCall(t, f.app, "DELETE", path, auth, "", 200)
	passCall(t, f.app, "GET", updates, auth, "", 401)
}

func TestPGPasskitCanonicalProfileMediaAndConcurrentIssuance(t *testing.T) {
	f := newPasskitFixture(t)
	var wg sync.WaitGroup
	out := make(chan passkit.Bundle, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b, e := f.passes.Member(f.ctx, f.actor); out <- b; errs <- e }()
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	token, tag := "", ""
	for b := range out {
		got := passToken(t, b.Data)
		if token != "" && (got != token || b.Tag != tag) {
			t.Fatal("concurrent issuance changed token or tag")
		}
		token, tag = got, b.Tag
	}
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_apple_passes WHERE account_id=$1::uuid`, f.actor).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	data := profilePNG(t)
	file, _, e := f.repo.Upload(f.ctx, f.actor, media.Upload{Name: "synthetic.png", MIMEType: "image/png", Size: int64(len(data)), SHA256: mediaDigest(string(data)), IdempotencyKey: "pass-avatar"}, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_account_profile_media(account_id,slot,file_id,attached_at)VALUES($1::uuid,'picture',$2::uuid,clock_timestamp())`, f.actor, file.ID)
	b, e := f.passes.Member(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	files := passFiles(t, b.Data)
	if len(files["thumbnail@3x.png"]) == 0 || b.Tag == tag {
		t.Fatal("profile media was not included")
	}
	if _, e = f.repo.Delete(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal(e)
	}
	after, e := f.passes.Latest(context.Background(), passType(f), f.actor, token)
	if e != nil {
		t.Fatal(e)
	}
	if len(passFiles(t, after.Data)["thumbnail@3x.png"]) != 0 || after.Tag == b.Tag {
		t.Fatal("deleted canonical profile image retained")
	}
}

type blockingPassSigner struct {
	passkit.ManifestSigner
	entered, release chan struct{}
	fail             bool
}

func (s blockingPassSigner) SignManifest(ctx context.Context, b []byte, now time.Time) ([]byte, error) {
	signed, e := s.ManifestSigner.SignManifest(ctx, b, now)
	if e != nil {
		return nil, e
	}
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.fail {
		return nil, errors.New("synthetic signer failure")
	}
	return signed, nil
}
func TestPGPasskitSigningRollbackAndCurrentIdentity(t *testing.T) {
	f := newPasskitFixture(t)
	c := f.signing.Config
	c.Signer = blockingPassSigner{ManifestSigner: c.Signer, fail: true}
	g, e := passkit.NewGenerator(c)
	if e != nil {
		t.Fatal(e)
	}
	repo, e := passkitpg.New(f.db, f.guard, passkitpg.Config{Generator: g})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Member(f.ctx, f.actor); e == nil {
		t.Fatal("failed signer succeeded")
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_apple_passes WHERE account_id=$1::uuid`, f.actor).Scan(&count); e != nil || count != 0 {
		t.Fatal("sign failure persisted a pass", count, e)
	}
	var now atomic.Int64
	now.Store(f.config.Now().Unix())
	cfg := f.config
	cfg.Now = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	v, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), v)
	request := httptest.NewRequest("GET", "/api/accounts/me/passbook/member", nil)
	request.Header.Set("Authorization", "Bearer "+f.token)
	proof, e := v.AuthenticateRequest(request)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 10*time.Second)
	defer cancel()
	blocked := blockingPassSigner{ManifestSigner: f.signing.Config.Signer, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release) }) }
	defer release()
	c = f.signing.Config
	c.Signer = blocked
	g, e = passkit.NewGenerator(c)
	if e != nil {
		t.Fatal(e)
	}
	repo, e = passkitpg.New(f.db, guard, passkitpg.Config{Generator: g})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := repo.Member(ctx, f.actor); done <- e }()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("signer wait not reached", ctx.Err())
	}
	now.Add(24 * 60 * 60)
	release()
	if e = <-done; e == nil {
		t.Fatal("expired canonical credential published pass")
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_apple_passes WHERE account_id=$1::uuid`, f.actor).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired issuance persisted", count, e)
	}
}

func TestPGPasskitWaitRechecksAccountAndCanonicalMedia(t *testing.T) {
	for _, operation := range []string{"member", "callback"} {
		t.Run(operation, func(t *testing.T) {
			f := newPasskitFixture(t)
			b, e := f.passes.Member(f.ctx, f.actor)
			if e != nil {
				t.Fatal(e)
			}
			token := passToken(t, b.Data)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
			if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "member" {
					_, e := f.passes.Member(ctx, f.actor)
					done <- e
				} else {
					_, e := f.passes.Latest(ctx, passType(f), f.actor, token)
					done <- e
				}
			}()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("disabled owner accepted after account wait")
			}
		})
	}
	f := newPasskitFixture(t)
	data := profilePNG(t)
	file, _, e := f.repo.Upload(f.ctx, f.actor, media.Upload{Name: "wait.png", MIMEType: "image/png", Size: int64(len(data)), SHA256: mediaDigest(string(data)), IdempotencyKey: "pass-media-wait"}, bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_account_profile_media(account_id,slot,file_id,attached_at)VALUES($1::uuid,'picture',$2::uuid,clock_timestamp())`, f.actor, file.ID)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
	done := make(chan passkit.Bundle, 1)
	errs := make(chan error, 1)
	go func() { b, e := f.passes.Member(ctx, f.actor); done <- b; errs <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting' WHERE id=$1::uuid`, file.ID); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	b := <-done
	if e = <-errs; e != nil {
		t.Fatal(e)
	}
	if len(passFiles(t, b.Data)["thumbnail.png"]) != 0 {
		t.Fatal("stale image retained after file wait")
	}
}

func TestPGPasskitRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "apple-pass-record.json"
	type saved struct {
		Actor, Type, Token, Tag        string
		Key, Certificate, Intermediate []byte
	}
	if phase == "before" {
		f := newPasskitFixture(t)
		b, e := f.passes.Member(f.ctx, f.actor)
		if e != nil {
			t.Fatal(e)
		}
		state := saved{f.actor, passType(f), passToken(t, b.Data), b.Tag, x509.MarshalPKCS1PrivateKey(f.signing.Key), f.signing.Certificate.Raw, f.signing.Intermediate.Raw}
		if _, e = f.passes.Register(context.Background(), state.Type, state.Actor, state.Token, "synthetic-restart-device", "synthetic-restart-push"); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, state)
		return
	}
	var state saved
	loadRestartFixture(t, marker, &state)
	key, e := x509.ParsePKCS1PrivateKey(state.Key)
	if e != nil {
		t.Fatal(e)
	}
	certificate, e := x509.ParseCertificate(state.Certificate)
	if e != nil {
		t.Fatal(e)
	}
	intermediate, e := x509.ParseCertificate(state.Intermediate)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := passkit.NewCMS(key, certificate, intermediate, state.Type, "SYNTHETIC1")
	if e != nil {
		t.Fatal(e)
	}
	generator, e := passkit.NewGenerator(testpasskit.Config(t, signer))
	if e != nil {
		t.Fatal(e)
	}
	var now atomic.Int64
	now.Store(time.Now().Unix())
	cfg := authorityConfig(t, authorityKeys(t), &now)
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	repo, e := passkitpg.New(db, authoritypg.NewTransactionGuard(authoritypg.New(db), v), passkitpg.Config{Generator: generator})
	if e != nil {
		t.Fatal(e)
	}
	serials, e := repo.Updates(context.Background(), state.Type, "synthetic-restart-device", state.Token, "")
	if e != nil || len(serials.SerialNumbers) != 1 || serials.SerialNumbers[0] != state.Actor || serials.LastUpdated != state.Tag {
		t.Fatal("registration or update state lost after restart", serials, e)
	}
	b, e := repo.Latest(context.Background(), state.Type, state.Actor, state.Token)
	if e != nil {
		t.Fatal(e)
	}
	if b.Tag != state.Tag || passToken(t, b.Data) != state.Token || passJSON(t, b.Data)["serialNumber"] != state.Actor {
		t.Fatal("pass identity changed on restart")
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM owned_apple_passes WHERE account_id=$1::uuid`, state.Actor).Scan(&count); e != nil || count != 1 {
		t.Fatal("restart duplicated pass", count, e)
	}
}
