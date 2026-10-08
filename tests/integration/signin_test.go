// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticPassword = "synthetic password only 1234"

type syntheticDelivery struct {
	mu     sync.Mutex
	tokens map[string]string
	fail   bool
}

func (d *syntheticDelivery) Send(ctx context.Context, email, purpose, code string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !strings.HasSuffix(email, "@example.invalid") {
		return errors.New("synthetic destination required")
	}
	if d.fail {
		return errors.New("synthetic delivery failure")
	}
	d.tokens[email+":"+purpose] = code
	return nil
}
func (d *syntheticDelivery) code(email, purpose string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tokens[email+":"+purpose]
}

type syntheticCaptcha struct{}

func (syntheticCaptcha) Verify(ctx context.Context, token, peer string) error {
	if token != "synthetic-captcha" {
		return signin.ErrInvalid
	}
	return nil
}

type signinFixture struct {
	db       *sql.DB
	ctx      context.Context
	now      *atomic.Int64
	keys     string
	issuer   *authority.Service
	verifier *authority.Verifier
	service  *signin.Service
	delivery *syntheticDelivery
	config   signin.Config
	store    *signinpg.Repository
}

func newSigninFixture(t *testing.T) *signinFixture {
	t.Helper()
	f := &signinFixture{db: database(t), ctx: context.Background(), now: &atomic.Int64{}, keys: authorityKeys(t), delivery: &syntheticDelivery{tokens: map[string]string{}}}
	f.now.Store(time.Now().UTC().Unix())
	if _, e := f.db.Exec(authoritypg.Schema + signinpg.Schema); e != nil {
		t.Fatal(e)
	}
	f.issuer = newPGAuthority(t, f.db, f.keys, f.now)
	canonical := authoritypg.New(f.db)
	cfg := authorityConfig(t, f.keys, f.now)
	var e error
	f.verifier, e = authority.NewVerifier(canonical, authority.Config{Issuer: cfg.Issuer, Audience: cfg.Audience, TrustedKeys: cfg.TrustedKeys, Now: cfg.Now})
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(canonical, f.verifier)
	f.store = signinpg.New(f.db, canonical, guard)
	seal := sha256.Sum256(append(append([]byte(nil), cfg.ReplaySealKey...), []byte("synthetic-signin-only")...))
	f.config = signin.Config{SealKey: seal[:], Issuer: "Synthetic Signin", Delivery: f.delivery, Captcha: syntheticCaptcha{}, Now: cfg.Now}
	f.service, e = signin.New(f.store, f.issuer, f.verifier, f.config)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *signinFixture) register(t *testing.T, verified bool) (authority.Account, string) {
	t.Helper()
	name := "synthetic_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:20]
	email := name + "@example.invalid"
	a, e := f.service.Register(f.ctx, signin.RegisterRequest{Name: name, Nick: "Synthetic", Email: email, Password: syntheticPassword, CaptchaToken: "synthetic-captcha"}, name)
	if e != nil {
		t.Fatal(e)
	}
	if verified {
		if e = f.service.VerifyEmail(f.ctx, f.delivery.code(email, "verify_email")); e != nil {
			t.Fatal(e)
		}
	}
	return a, email
}
func (f *signinFixture) challenge(t *testing.T, name string) (signin.Challenge, signin.Factor) {
	t.Helper()
	c, e := f.service.Start(f.ctx, signin.StartRequest{Account: name, DeviceName: "Synthetic client"}, name)
	if e != nil {
		t.Fatal(e)
	}
	fs, e := f.service.ChallengeFactors(f.ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range fs {
		if v.Type == 0 {
			return c, v
		}
	}
	t.Fatal("missing password factor")
	return c, signin.Factor{}
}
func (f *signinFixture) login(t *testing.T, name, password string) authority.Pair {
	t.Helper()
	c, p := f.challenge(t, name)
	done, e := f.service.VerifyFactor(f.ctx, c.ID, p.ID, password, name)
	if e != nil || done.StepRemain != 0 {
		t.Fatal("password login failed", e)
	}
	pair, e := f.issuer.ExchangeCode(f.ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	return pair
}
func TestPGSigninLifecycle(t *testing.T) {
	f := newSigninFixture(t)
	a, email := f.register(t, false)
	duplicateName := "synthetic_duplicate_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:12]
	if _, e := f.service.Register(f.ctx, signin.RegisterRequest{Name: duplicateName, Email: email, Password: syntheticPassword, CaptchaToken: "synthetic-captcha"}, duplicateName); !errors.Is(e, signin.ErrConflict) {
		t.Fatal("duplicate email accepted", e)
	}
	var orphan int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_authority_accounts WHERE name=$1`, duplicateName).Scan(&orphan); e != nil || orphan != 0 {
		t.Fatal("failed registration left canonical orphan", e)
	}
	if _, e := f.service.Start(f.ctx, signin.StartRequest{Account: a.Name}, a.Name); !errors.Is(e, signin.ErrUnverified) {
		t.Fatal("unverified login", e)
	}
	verify := f.delivery.code(email, "verify_email")
	if e := f.service.ResetPassword(f.ctx, verify, "different synthetic password"); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("cross-purpose reset", e)
	}
	if e := f.service.VerifyEmail(f.ctx, verify); e != nil {
		t.Fatal(e)
	}
	if e := f.service.VerifyEmail(f.ctx, verify); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("verification replay", e)
	}
	c, p := f.challenge(t, a.Name)
	if _, e := f.issuer.ExchangeCode(f.ctx, c.ID); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unfinished challenge issued token", e)
	}
	if _, e := f.service.VerifyFactor(f.ctx, c.ID, p.ID, "wrong synthetic password", a.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal(e)
	}
	failed, e := f.service.Challenge(f.ctx, c.ID)
	if e != nil || failed.FailedAttempts != 1 || failed.StepTotal != 1 {
		t.Fatal("failed attempt DTO was not durable", e)
	}
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_authority_codes WHERE account_id=$1`, a.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed factor published code", e)
	}
	done, e := f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name)
	if e != nil || done.StepRemain != 0 || len(done.BlacklistFactors) != 1 {
		t.Fatal("factor completion", e)
	}
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("factor replay", e)
	}
	pair, e := f.issuer.ExchangeCode(f.ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.issuer.ExchangeCode(f.ctx, c.ID); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("token code replay", e)
	}
	principal, e := f.verifier.Authenticate(f.ctx, pair.Token)
	if e != nil || principal.Account.ID != a.ID {
		t.Fatal("canonical account mismatch", e)
	}
	if _, e = f.verifier.Authorize(f.ctx, pair.Token, "posts.create"); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("registration implicitly granted permissions", e)
	}
	var persisted []byte
	if e = f.db.QueryRow(`SELECT secret FROM owned_authority_factors WHERE account_id=$1 AND type=0`, a.ID).Scan(&persisted); e != nil || bytes.Contains(persisted, []byte(syntheticPassword)) || !bytes.HasPrefix(persisted, []byte("pbkdf2-sha256$600000$")) {
		t.Fatal("password persistence", e)
	}
	if e = f.service.RequestReset(f.ctx, a.Name, "synthetic-captcha", a.Name); e != nil {
		t.Fatal(e)
	}
	reset := f.delivery.code(email, "reset_password")
	if reset == "" {
		t.Fatal("no synthetic reset delivery")
	}
	const replacement = "replacement synthetic password 1234"
	if e = f.service.ResetPassword(f.ctx, reset, replacement); e != nil {
		t.Fatal(e)
	}
	if e = f.service.ResetPassword(f.ctx, reset, replacement); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("reset replay", e)
	}
	if _, e = f.verifier.Authenticate(f.ctx, pair.Token); !errors.Is(e, authority.ErrStale) {
		t.Fatal("reset retained access", e)
	}
	if _, e = f.issuer.Refresh(f.ctx, pair.RefreshToken, 0, ""); !errors.Is(e, authority.ErrStale) {
		t.Fatal("reset retained refresh", e)
	}
	c, p = f.challenge(t, a.Name)
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("old password survived reset", e)
	}
	f.login(t, a.Name, replacement)
	other, _ := f.register(t, true)
	otherChallenge, otherFactor := f.challenge(t, other.Name)
	if _, e = f.service.VerifyFactor(f.ctx, otherChallenge.ID, p.ID, replacement, other.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("cross-account factor", e)
	}
	f.now.Add(301)
	if _, e = f.service.VerifyFactor(f.ctx, otherChallenge.ID, otherFactor.ID, syntheticPassword, other.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("expired challenge", e)
	}
	failedName := "synthetic_delivery_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:12]
	failedEmail := failedName + "@example.invalid"
	f.delivery.fail = true
	if _, e = f.service.Register(f.ctx, signin.RegisterRequest{Name: failedName, Email: failedEmail, Password: syntheticPassword, CaptchaToken: "synthetic-captcha"}, failedName); !errors.Is(e, signin.ErrDelivery) {
		t.Fatal("failed delivery reported success", e)
	}
	f.delivery.fail = false
	if _, e = f.service.Start(f.ctx, signin.StartRequest{Account: failedName}, failedName); !errors.Is(e, signin.ErrUnverified) {
		t.Fatal("failed delivery enabled login", e)
	}
	if e = f.service.ResendVerification(f.ctx, failedName, "synthetic-captcha", failedName); e != nil {
		t.Fatal(e)
	}
	if e = f.service.VerifyEmail(f.ctx, f.delivery.code(failedEmail, "verify_email")); e != nil {
		t.Fatal(e)
	}
	f.login(t, failedName, syntheticPassword)
}
func syntheticTOTP(t *testing.T, uri string, now int64) string {
	t.Helper()
	u, e := url.Parse(uri)
	if e != nil {
		t.Fatal(e)
	}
	key, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(u.Query().Get("secret"))
	if e != nil || len(key) != 20 {
		t.Fatal("invalid synthetic TOTP URI", e)
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(now/30))
	h := hmac.New(sha1.New, key)
	_, _ = h.Write(b[:])
	v := h.Sum(nil)
	o := v[len(v)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(v[o:o+4])&0x7fffffff)%1000000)
}
func TestPGSigninTOTPAndRecovery(t *testing.T) {
	f := newSigninFixture(t)
	a, _ := f.register(t, true)
	pair := f.login(t, a.Name, syntheticPassword)
	recovery, e := f.service.CreateFactor(f.ctx, pair.Token, 5, "")
	if e != nil {
		t.Fatal(e)
	}
	recoveryCode := recovery.CreatedResponse["recovery_code"]
	totp, e := f.service.CreateFactor(f.ctx, pair.Token, 3, "")
	if e != nil {
		t.Fatal(e)
	}
	uri := totp.CreatedResponse["uri"]
	fs, e := f.service.Factors(f.ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range fs {
		if len(v.Secret) != 0 || v.CreatedResponse != nil {
			t.Fatal("factor list leaked secrets")
		}
	}
	if _, e = f.service.EnableFactor(f.ctx, pair.Token, totp.ID, "badcode"); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("invalid TOTP enabled", e)
	}
	if _, e = f.service.EnableFactor(f.ctx, pair.Token, totp.ID, syntheticTOTP(t, uri, f.now.Load())); e != nil {
		t.Fatal(e)
	}
	if _, e = f.verifier.Authenticate(f.ctx, pair.Token); !errors.Is(e, authority.ErrStale) {
		t.Fatal("enrollment retained old session", e)
	}
	c, p := f.challenge(t, a.Name)
	if c.StepRemain != 2 {
		t.Fatal("MFA not required")
	}
	done, e := f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name)
	if e != nil || done.StepRemain != 1 {
		t.Fatal(e)
	}
	if _, e = f.issuer.ExchangeCode(f.ctx, c.ID); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("password bypassed MFA", e)
	}
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, totp.ID, syntheticTOTP(t, uri, f.now.Load()), a.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("enrollment TOTP reused", e)
	}
	f.now.Add(30)
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, totp.ID, syntheticTOTP(t, uri, f.now.Load()), a.Name); e != nil {
		t.Fatal(e)
	}
	mfa, e := f.issuer.ExchangeCode(f.ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	c, p = f.challenge(t, a.Name)
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, totp.ID, syntheticTOTP(t, uri, f.now.Load()), a.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("TOTP cross-challenge replay", e)
	}
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, recovery.ID, recoveryCode, a.Name); e != nil {
		t.Fatal(e)
	}
	rescued, e := f.issuer.ExchangeCode(f.ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	c, p = f.challenge(t, a.Name)
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, recovery.ID, recoveryCode, a.Name); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("recovery factor reused", e)
	}
	if e = f.service.DeleteFactor(f.ctx, rescued.Token, totp.ID); e != nil {
		t.Fatal(e)
	}
	for _, token := range []string{rescued.Token, mfa.Token} {
		if _, e = f.verifier.Authenticate(f.ctx, token); !errors.Is(e, authority.ErrStale) {
			t.Fatal("factor removal retained session", e)
		}
	}
	f.login(t, a.Name, syntheticPassword)
}

type failCompletionStore struct{ signin.Store }
type failCompletionTx struct{ signin.Transaction }

func (s failCompletionStore) Within(ctx context.Context, id string, fn func(signin.Transaction) error) error {
	return s.Store.Within(ctx, id, func(tx signin.Transaction) error { return fn(failCompletionTx{tx}) })
}
func (t failCompletionTx) SaveChallenge(c signin.Challenge) error {
	if c.Completed {
		return errors.New("synthetic rollback after code publication")
	}
	return t.Transaction.SaveChallenge(c)
}

func TestPGSigninRateConcurrency(t *testing.T) {
	f := newSigninFixture(t)
	a, email := f.register(t, true)
	c, p := f.challenge(t, a.Name)
	broken, e := signin.New(failCompletionStore{f.store}, f.issuer, f.verifier, f.config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = broken.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("expected injected completion failure", e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_authority_codes WHERE account_id=$1`, a.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("transaction published code despite rollback", e)
	}
	snapshot, e := f.service.Challenge(f.ctx, c.ID)
	if e != nil || snapshot.StepRemain != 1 {
		t.Fatal("challenge changed despite rollback", e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, signin.ErrInvalid) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("completion was not one-use")
	}
	if _, e = f.issuer.ExchangeCode(f.ctx, c.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.service.RequestReset(f.ctx, a.Name, "synthetic-captcha", a.Name); e != nil {
		t.Fatal(e)
	}
	raw := f.delivery.code(email, "reset_password")
	results = make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.service.ResetPassword(f.ctx, raw, "concurrent synthetic replacement")
		}()
	}
	wg.Wait()
	close(results)
	success = 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, signin.ErrInvalid) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("reset was not one-use")
	}
	other, _ := f.register(t, true)
	c, p = f.challenge(t, other.Name)
	for i := 0; i < 8; i++ {
		if _, e = f.service.VerifyFactor(f.ctx, c.ID, p.ID, "wrong password", other.Name); !errors.Is(e, signin.ErrInvalid) {
			t.Fatal("attempt", i, e)
		}
	}
	restarted, e := signin.New(f.store, f.issuer, f.verifier, f.config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, other.Name); !errors.Is(e, signin.ErrRate) {
		t.Fatal("rate limit lost across reconstruction", e)
	}
	f.now.Add(601)
	f.login(t, other.Name, syntheticPassword)
}

func signinHTTP(h http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestPGSigninHTTP(t *testing.T) {
	f := newSigninFixture(t)
	mux := http.NewServeMux()
	f.service.RegisterRoutes(mux)
	mux.Handle("/", f.issuer.Handler())
	name := "synthetic_http_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:16]
	email := name + "@example.invalid"
	w := signinHTTP(mux, "POST", "/stargate/accounts", signin.RegisterRequest{Name: name, Email: email, Password: syntheticPassword, CaptchaToken: "synthetic-captcha"}, "")
	if w.Code != 202 {
		t.Fatal("register HTTP", w.Code)
	}
	w = signinHTTP(mux, "POST", "/stargate/accounts/verify", map[string]string{"code": f.delivery.code(email, "verify_email")}, "")
	if w.Code != 204 {
		t.Fatal("verify HTTP", w.Code)
	}
	w = signinHTTP(mux, "POST", "/stargate/auth/challenge", signin.StartRequest{Account: name, DeviceID: "synthetic-device", DeviceName: "Synthetic HTTP", Platform: 1}, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("challenge HTTP", w.Code)
	}
	var c signin.Challenge
	if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	var dto map[string]json.RawMessage
	if e := json.Unmarshal(w.Body.Bytes(), &dto); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"id", "step_remain", "step_total", "failed_attempts", "blacklist_factors", "audiences", "scopes", "ip_address", "user_agent", "account_id", "created_at", "updated_at"} {
		if len(dto[key]) == 0 || bytes.Equal(dto[key], []byte("null")) {
			t.Fatal("missing authored challenge field", key)
		}
	}
	if c.StepTotal != 1 || c.IPAddress != "192.0.2.1" || c.DeviceID != "synthetic-device" {
		t.Fatal("challenge request metadata was not persisted")
	}
	w = signinHTTP(mux, "GET", "/stargate/auth/challenge/"+c.ID+"/factors", nil, "")
	if w.Code != 200 {
		t.Fatal("factors HTTP", w.Code)
	}
	var fs []signin.Factor
	if e := json.Unmarshal(w.Body.Bytes(), &fs); e != nil || len(fs) != 1 {
		t.Fatal("factor DTO", e)
	}
	w = signinHTTP(mux, "POST", "/stargate/auth/challenge/"+c.ID+"/factors/"+fs[0].ID, nil, "")
	if w.Code != 204 {
		t.Fatal("prepare HTTP", w.Code)
	}
	w = signinHTTP(mux, "PATCH", "/stargate/auth/challenge/"+c.ID, map[string]string{"factor_id": fs[0].ID, "password": syntheticPassword}, "")
	if w.Code != 200 {
		t.Fatal("verify factor HTTP", w.Code)
	}
	w = signinHTTP(mux, "POST", "/stargate/auth/token", map[string]string{"grant_type": "authorization_code", "code": c.ID}, "")
	if w.Code != 200 {
		t.Fatal("token HTTP", w.Code)
	}
	var pair authority.Pair
	if e := json.Unmarshal(w.Body.Bytes(), &pair); e != nil {
		t.Fatal(e)
	}
	w = signinHTTP(mux, "GET", "/stargate/accounts/me", nil, pair.Token)
	if w.Code != 200 {
		t.Fatal("canonical current-account HTTP", w.Code)
	}
	w = signinHTTP(mux, "POST", "/passport/accounts/recovery/password", map[string]string{"account": name, "captcha_token": "synthetic-captcha"}, "")
	if w.Code != 202 || strings.Contains(w.Body.String(), f.delivery.code(email, "reset_password")) {
		t.Fatal("recovery leaked token or failed", w.Code)
	}
	w = signinHTTP(mux, "PATCH", "/passport/accounts/recovery/password", map[string]string{"code": f.delivery.code(email, "reset_password"), "password": "HTTP replacement synthetic password"}, "")
	if w.Code != 204 {
		t.Fatal("reset HTTP", w.Code)
	}
	w = signinHTTP(mux, "GET", "/stargate/accounts/me", nil, pair.Token)
	if w.Code != 401 {
		t.Fatal("HTTP reset retained session", w.Code)
	}
	unavailableConfig := f.config
	unavailableConfig.Delivery = nil
	unavailable, e := signin.New(f.store, f.issuer, f.verifier, unavailableConfig)
	if e != nil {
		t.Fatal(e)
	}
	w = signinHTTP(unavailable.Handler(), "POST", "/passport/accounts/recovery/password", map[string]string{"account": name, "captcha_token": "synthetic-captcha"}, "")
	if w.Code != 503 {
		t.Fatal("pretended recovery delivery", w.Code)
	}
	w = signinHTTP(unavailable.Handler(), "POST", "/stargate/accounts", signin.RegisterRequest{Name: "synthetic_no_delivery", Email: "synthetic_no_delivery@example.invalid", Password: syntheticPassword, CaptchaToken: "synthetic-captcha"}, "")
	if w.Code != 503 {
		t.Fatal("pretended enrollment delivery", w.Code)
	}
	w = signinHTTP(mux, "POST", "/stargate/factors", map[string]any{"type": 7}, pair.Token)
	if w.Code == 200 || w.Code == 201 {
		t.Fatal("unsupported passkey success")
	}
	for _, keys := range [][]string{{"*"}, {"posts.*"}, {"INVALID"}, {"posts.create", "posts.create"}} {
		cfg := f.config
		cfg.InitialMemberPermissions = keys
		if _, e = signin.New(f.store, f.issuer, f.verifier, cfg); !errors.Is(e, signin.ErrInput) {
			t.Fatal("invalid initial member policy", e)
		}
	}
	policy := []string{"posts.create"}
	cfg := f.config
	cfg.InitialMemberPermissions = policy
	f.service, e = signin.New(f.store, f.issuer, f.verifier, cfg)
	if e != nil {
		t.Fatal(e)
	}
	policy[0] = "posts.delete"
	member, _ := f.register(t, true)
	memberPair := f.login(t, member.Name, syntheticPassword)
	if _, e = f.verifier.Authorize(f.ctx, memberPair.Token, "posts.create"); e != nil {
		t.Fatal("configured member grant missing", e)
	}
	if _, e = f.verifier.Authorize(f.ctx, memberPair.Token, "posts.delete"); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("mutable policy or extra grant", e)
	}
}

type pauseSigninStore struct {
	signin.Store
	entered chan struct{}
	resume  chan struct{}
}

func (s *pauseSigninStore) Bind(ctx context.Context, p authority.Principal) context.Context {
	close(s.entered)
	<-s.resume
	return s.Store.Bind(ctx, p)
}
func TestPGSigninAuthorityBoundary(t *testing.T) {
	f := newSigninFixture(t)
	a, _ := f.register(t, true)
	b, _ := f.register(t, true)
	pa := f.login(t, a.Name, syntheticPassword)
	pb := f.login(t, b.Name, syntheticPassword)
	factor, e := f.service.CreateFactor(f.ctx, pa.Token, 3, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.EnableFactor(f.ctx, pb.Token, factor.ID, "123456"); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("cross-account enable", e)
	}
	if e = f.service.DeleteFactor(f.ctx, pb.Token, factor.ID); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("cross-account delete", e)
	}
	c, p := f.challenge(t, a.Name)
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, a.ID, nil, []authority.Punishment{{Kind: "block_login"}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, p.ID, syntheticPassword, a.Name); !errors.Is(e, authority.ErrDisabled) {
		t.Fatal("blocked login completed", e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, a.ID, nil, nil); e != nil {
		t.Fatal(e)
	}
	paused := &pauseSigninStore{Store: f.store, entered: make(chan struct{}), resume: make(chan struct{})}
	s, e := signin.New(paused, f.issuer, f.verifier, f.config)
	if e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := s.CreateFactor(f.ctx, pa.Token, 5, ""); result <- e }()
	select {
	case <-paused.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guard test did not enter")
	}
	if e = f.issuer.Revoke(f.ctx, pa.Token, "current"); e != nil {
		t.Fatal(e)
	}
	close(paused.resume)
	select {
	case e = <-result:
		if !errors.Is(e, authority.ErrRevoked) {
			t.Fatal("revocation between checks not enforced", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guard test did not complete")
	}
	var n int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_authority_factors WHERE account_id=$1 AND type=5`, a.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("revoked session persisted factor", e)
	}
	f.now.Add(601)
	fresh, e := f.issuer.Refresh(f.ctx, pb.RefreshToken, 0, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.CreateFactor(f.ctx, fresh.Token, 3, ""); !errors.Is(e, signin.ErrRecent) {
		t.Fatal("refresh bypassed recent login", e)
	}
}

// The after phase is read-only and cannot recreate missing authentication state.
func TestPGSigninRestartPersistence(t *testing.T) {
	db := database(t)
	const account = "a0710000-0000-4000-8000-000000000001"
	const factor = "a0710000-0000-4000-8000-000000000002"
	bucket := sha256.Sum256([]byte("synthetic-signin-restart-bucket"))
	challenge := sha256.Sum256([]byte("synthetic-signin-restart-challenge"))
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		query(t, db, `INSERT INTO owned_authority_accounts(id,name,nick,version,created_at) VALUES($1,'synthetic_signin_restart','Synthetic',7,'2026-01-01')`, account)
		query(t, db, `INSERT INTO owned_authority_credentials(account_id,email,verified_at) VALUES($1,'synthetic_restart@example.invalid','2026-01-01')`, account)
		query(t, db, `INSERT INTO owned_authority_factors(id,account_id,type,created_at,enabled_at,secret,last_counter) VALUES($1,$2,3,'2026-01-01','2026-01-01',$3,777)`, factor, account, []byte("synthetic sealed fixture only"))
		query(t, db, `INSERT INTO owned_authority_challenges(hash,account_id,account_version,purpose,created_at,expires_at,completed) VALUES($1,$2,7,'reset_password','2026-01-01','2026-01-02',true)`, challenge[:], account)
		query(t, db, `INSERT INTO owned_authority_attempts(bucket,attempts,window_end) VALUES($1,9,'2027-01-01')`, bucket[:])
	case "after":
	default:
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	var version, counter, attempts int
	var verified, completed bool
	e := db.QueryRow(`SELECT a.version,c.verified_at IS NOT NULL,f.last_counter,h.completed,(SELECT attempts FROM owned_authority_attempts WHERE bucket=$3) FROM owned_authority_accounts a JOIN owned_authority_credentials c ON c.account_id=a.id JOIN owned_authority_factors f ON f.account_id=a.id JOIN owned_authority_challenges h ON h.account_id=a.id WHERE a.id=$1 AND h.hash=$2`, account, challenge[:], bucket[:]).Scan(&version, &verified, &counter, &completed, &attempts)
	if e != nil || version != 7 || !verified || counter != 777 || !completed || attempts != 9 {
		t.Fatal("signin state lost across PostgreSQL restart", e)
	}
}
