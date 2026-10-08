// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"testing"
	"time"
)

const syntheticPaymentPIN = "481527"

type paymentPINFixture struct {
	*signinFixture
	guard      *authoritypg.TransactionGuard
	pin        *walletpg.PINVerifier
	account    authority.Account
	pair       authority.Pair
	passwordID string
	peppers    map[string][]byte
}

func newPaymentPINFixture(t *testing.T) *paymentPINFixture {
	t.Helper()
	f := &paymentPINFixture{signinFixture: newSigninFixture(t)}
	canonical := authoritypg.New(f.db)
	f.guard = authoritypg.NewTransactionGuard(canonical, f.verifier)
	pepper := sha256.Sum256([]byte("owned synthetic disposable payment PIN pepper"))
	f.peppers = map[string][]byte{"synthetic-pin": pepper[:]}
	f.pin = walletpg.NewPINVerifier(f.db, f.peppers).WithAuthority(f.guard).WithClock(f.config.Now).WithEnrollmentPepper("synthetic-pin")
	f.store = signinpg.New(f.db, canonical, f.guard).WithPaymentPIN(f.pin)
	var e error
	f.service, e = signin.New(f.store, f.issuer, f.verifier, f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.account, _ = f.register(t, true)
	if e = canonical.ReplacePolicy(f.ctx, f.account.ID, []authority.Rule{{Pattern: "wallets.*"}, {Pattern: "orders.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.relogin(t)
	fs, e := f.service.Factors(f.ctx, f.pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	for _, factor := range fs {
		if factor.Type == 0 {
			f.passwordID = factor.ID
		}
	}
	return f
}
func (f *paymentPINFixture) relogin(t *testing.T) {
	t.Helper()
	f.pair = f.login(t, f.account.Name, syntheticPassword)
}
func (f *paymentPINFixture) enroll(t *testing.T) signin.Factor {
	t.Helper()
	v, e := f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN)
	if e != nil {
		t.Fatal(e)
	}
	f.relogin(t)
	return v
}
func (f *paymentPINFixture) bound(t *testing.T) (context.Context, wallet.Principal) {
	t.Helper()
	p, e := f.verifier.Authenticate(f.ctx, f.pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	return f.guard.Bind(f.ctx, p), wallet.Principal{AccountID: p.Account.ID, SessionID: p.Session.ID}
}
func pinHTTP(t *testing.T, h http.Handler, method, path, token string, body any, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := fixtureJSONRequest(t, method, path, token, body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatal(path, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Header())
	}
	return w
}
func pinTransferFixture(t *testing.T, f *paymentPINFixture) (*walletpg.Repository, wallet.TransferCommand) {
	t.Helper()
	r := walletpg.NewWithAuthority(f.db, f.guard).WithPaymentAuthorization(f.pin)
	payer := walletCreate(t, walletpg.New(f.db), f.account.ID)
	payee := walletCreate(t, walletpg.New(f.db), authorityUUID(t))
	walletFund(t, f.db, payer, "synthetic-pin", "10")
	return r, wallet.TransferCommand{ActorID: f.account.ID, PayerWalletID: payer.ID, PayeeWalletID: payee.ID, PayerOwner: wallet.Owner{AccountID: f.account.ID}, Amount: walletAmount(t, "1"), Currency: "synthetic-pin", Fingerprint: strings.Repeat("a", 64), IdempotencyKey: "pin-payment"}
}

func TestPGPaymentPINSourceLifecycleAndPrivateVerifier(t *testing.T) {
	f := newPaymentPINFixture(t)
	for _, pin := range []string{"12345", "1234567", "１２３４５６", "12 456"} {
		if _, e := f.service.CreateFactor(f.ctx, f.pair.Token, 4, pin); !errors.Is(e, signin.ErrInput) {
			t.Fatal(pin, e)
		}
	}
	factor := f.enroll(t)
	if factor.Type != 4 || factor.EnabledAt == nil || len(factor.Secret) != 0 || factor.CreatedResponse != nil || factor.Trustworthy != 0 {
		t.Fatal(factor)
	}
	var secret, salt, mac []byte
	var linked string
	if e := f.db.QueryRow(`SELECT f.secret,p.salt,p.pin_mac,p.factor_id::text FROM owned_authority_factors f JOIN wallet_v1.payment_policies p ON p.account_id=f.account_id WHERE f.id=$1::uuid`, factor.ID).Scan(&secret, &salt, &mac, &linked); e != nil || secret != nil || len(salt) != 32 || len(mac) != 32 || linked != factor.ID {
		t.Fatal("duplicated PIN store", e)
	}
	c, _ := f.challenge(t, f.account.Name)
	fs, e := f.service.ChallengeFactors(f.ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range fs {
		if v.Type == 4 {
			t.Fatal("payment PIN used as login factor")
		}
	}
	old := f.pair.Token
	if e = f.service.DisableFactor(f.ctx, old, factor.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.verifier.Authenticate(f.ctx, old); e == nil {
		t.Fatal("mutation retained old authorization")
	}
	f.relogin(t)
	_, p := f.bound(t)
	status, e := f.pin.Status(f.ctx, p)
	if e != nil || status.HasPIN || !status.ValidationRequired {
		t.Fatal(status, e)
	}
	if e = f.pin.VerifyPayment(f.ctx, p, "NO_PIN_PROVEDED"); e == nil {
		t.Fatal("disable granted no-PIN policy")
	}
	pinHTTP(t, f.service.Handler(), "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, nil, 400)
	if _, e = f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, "999999"); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal(e)
	}
	if _, e = f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticPaymentPIN); e != nil {
		t.Fatal("failed PIN consumed assurance", e)
	}
	f.relogin(t)
	if e = f.service.DeleteFactor(f.ctx, f.pair.Token, factor.ID); e != nil {
		t.Fatal(e)
	}
	f.relogin(t)
	_, p = f.bound(t)
	if e = f.pin.VerifyPayment(f.ctx, p, ""); e == nil {
		t.Fatal("delete silently removed payment verification")
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_policies WHERE account_id=$1::uuid AND disabled AND factor_id IS NULL AND pin_mac IS NULL`, f.account.ID); n != 1 {
		t.Fatal(n)
	}
}
func TestPGPaymentPINCredentialAssuranceOriginExpiryAndRefresh(t *testing.T) {
	f := newPaymentPINFixture(t)
	issued, e := f.issuer.IssueSession(f.ctx, f.account.ID, authority.SessionOptions{Label: "trusted synthetic issuer", Scopes: []string{"*"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.CreateFactor(f.ctx, issued.Token, 4, syntheticPaymentPIN); !errors.Is(e, signin.ErrReauthentication) {
		t.Fatal("generic issuer manufactured assurance", e)
	}
	code, e := f.issuer.CreateLoginCode(f.ctx, f.account.ID, "trusted synthetic code", []string{"*"})
	if e != nil {
		t.Fatal(e)
	}
	generic, e := f.issuer.ExchangeCode(f.ctx, code)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.CreateFactor(f.ctx, generic.Token, 4, syntheticPaymentPIN); !errors.Is(e, signin.ErrReauthentication) {
		t.Fatal("generic code manufactured assurance", e)
	}
	var originalTime time.Time
	var methods []byte
	if e = f.db.QueryRow(`SELECT auth_time,methods FROM owned_authority_credential_assurances WHERE account_id=$1::uuid`, f.account.ID).Scan(&originalTime, &methods); e != nil {
		t.Fatal(e)
	}
	f.now.Add(10)
	refreshed, e := f.issuer.Refresh(f.ctx, f.pair.RefreshToken, 0, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.CreateFactor(f.ctx, refreshed.Token, 4, syntheticPaymentPIN); !errors.Is(e, signin.ErrReauthentication) {
		t.Fatal("refresh manufactured assurance", e)
	}
	var afterTime time.Time
	var epoch int64
	if e = f.db.QueryRow(`SELECT auth_time,session_epoch FROM owned_authority_credential_assurances WHERE account_id=$1::uuid`, f.account.ID).Scan(&afterTime, &epoch); e != nil || !afterTime.Equal(originalTime) || epoch != 1 {
		t.Fatal(afterTime, epoch, e)
	}
	f.relogin(t)
	f.now.Add(301)
	if _, e = f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN); !errors.Is(e, signin.ErrReauthentication) {
		t.Fatal("expired assurance", e)
	}
	f.relogin(t)
	if _, e = f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN); e != nil {
		t.Fatal("ordinary login did not restore authored flow", e)
	}
}
func TestPGPaymentPINAssuranceRollbackAndConcurrentActions(t *testing.T) {
	f := newPaymentPINFixture(t)
	query(t, f.db, `CREATE FUNCTION synthetic_pin_audit_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.account_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic PIN rollback';END IF;RETURN NEW;END$$`)
	query(t, f.db, `CREATE TRIGGER synthetic_pin_audit_fail BEFORE INSERT ON owned_authority_factor_audit FOR EACH ROW EXECUTE FUNCTION synthetic_pin_audit_fail('`+f.account.ID+`')`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_pin_audit_fail ON owned_authority_factor_audit;DROP FUNCTION IF EXISTS synthetic_pin_audit_fail()`)
	})
	if _, e := f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN); e == nil {
		t.Fatal("rollback ignored")
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_policies WHERE account_id=$1::uuid`, f.account.ID); n != 0 {
		t.Fatal("policy escaped rollback")
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_authority_credential_assurances WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.account.ID); n != 0 {
		t.Fatal("assurance escaped rollback")
	}
	query(t, f.db, `DROP TRIGGER synthetic_pin_audit_fail ON owned_authority_factor_audit`)
	factor, e := f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN)
	if e != nil {
		t.Fatal(e)
	}
	f.relogin(t)
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; done <- f.service.DisableFactor(f.ctx, f.pair.Token, factor.ID) }()
	go func() { <-start; done <- f.service.DeleteFactor(f.ctx, f.pair.Token, factor.ID) }()
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		if e := <-done; e == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("one assurance authorized simultaneous actions", wins)
	}
	var action, target string
	var policy int64
	if e = f.db.QueryRow(`SELECT action,target_id::text,policy_version FROM owned_authority_credential_assurances WHERE account_id=$1::uuid AND action IN('disable','delete')`, f.account.ID).Scan(&action, &target, &policy); e != nil || target != factor.ID || policy < 1 {
		t.Fatal(action, target, policy, e)
	}
	bucket := sha256.Sum256([]byte("pin-lifecycle:" + f.account.ID))
	if n := commerceCount(t, f.db, `SELECT attempts FROM owned_authority_attempts WHERE bucket=$1::bytea`, bucket[:]); n < 3 {
		t.Fatal("rollback erased rates", n)
	}
}
func TestPGPaymentPINVerifiedAuthenticationTimeMFAAndRollback(t *testing.T) {
	f := newPaymentPINFixture(t)
	recovery, e := f.service.CreateFactor(f.ctx, f.pair.Token, 5, "")
	if e != nil {
		t.Fatal(e)
	}
	totp, e := f.service.CreateFactor(f.ctx, f.pair.Token, 3, "")
	if e != nil {
		t.Fatal(e)
	}
	uri := totp.CreatedResponse["uri"]
	if _, e = f.service.EnableFactor(f.ctx, f.pair.Token, totp.ID, syntheticTOTP(t, uri, f.now.Load())); e != nil {
		t.Fatal(e)
	}
	c, pwd := f.challenge(t, f.account.Name)
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, pwd.ID, syntheticPassword, f.account.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.issuer.ExchangeCode(f.ctx, c.ID); e == nil {
		t.Fatal("password bypassed MFA")
	}
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, totp.ID, syntheticTOTP(t, uri, f.now.Load()), f.account.ID); !errors.Is(e, signin.ErrInvalid) {
		t.Fatal("TOTP counter replay", e)
	}
	query(t, f.db, `ALTER TABLE owned_authority_challenges ADD CONSTRAINT synthetic_pin_completion_failure CHECK(account_id<>'`+f.account.ID+`'::uuid OR purpose<>'login' OR NOT completed) NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE owned_authority_challenges DROP CONSTRAINT IF EXISTS synthetic_pin_completion_failure`)
	})
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, recovery.ID, recovery.CreatedResponse["recovery_code"], f.account.ID); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("completion rollback not injected", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_authority_factors WHERE id=$1::uuid`, recovery.ID); n != 1 {
		t.Fatal("recovery consumed outside completion")
	}
	query(t, f.db, `ALTER TABLE owned_authority_challenges DROP CONSTRAINT synthetic_pin_completion_failure`)
	if _, e = f.service.VerifyFactor(f.ctx, c.ID, recovery.ID, recovery.CreatedResponse["recovery_code"], f.account.ID); e != nil {
		t.Fatal(e)
	}
	verifiedAt := time.Unix(f.now.Load(), 0)
	f.now.Add(31)
	f.pair, e = f.issuer.ExchangeCode(f.ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, p := f.bound(t)
	var authTime, created time.Time
	var methods []byte
	if e = f.db.QueryRow(`SELECT a.auth_time,a.methods,s.created_at FROM owned_authority_credential_assurances a JOIN owned_authority_sessions s ON s.id=a.session_id WHERE a.session_id=$1::uuid`, p.SessionID).Scan(&authTime, &methods, &created); e != nil || !authTime.Equal(verifiedAt) || !created.After(authTime) || !strings.Contains(string(methods), "pwd") || !strings.Contains(string(methods), "recovery") {
		t.Fatal(authTime, created, string(methods), e)
	}
	if _, e = f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN); e != nil {
		t.Fatal(e)
	}
	// An in-flight pre-upgrade challenge retains its already verified password
	// step, but cannot acquire new assurance from absent method metadata.
	legacy, pwd := f.challenge(t, f.account.Name)
	if _, e = f.service.VerifyFactor(f.ctx, legacy.ID, pwd.ID, syntheticPassword, f.account.ID); e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256([]byte(legacy.ID))
	query(t, f.db, `UPDATE owned_authority_challenges SET auth_methods='[]'::jsonb WHERE hash=$1::bytea`, hash[:])
	if _, e = f.service.VerifyFactor(f.ctx, legacy.ID, totp.ID, syntheticTOTP(t, uri, f.now.Load()), f.account.ID); e != nil {
		t.Fatal(e)
	}
	older, e := f.issuer.ExchangeCode(f.ctx, legacy.ID)
	if e != nil {
		t.Fatal("pre-upgrade challenge login lost", e)
	}
	principal, e := f.verifier.Authenticate(f.ctx, older.Token)
	if e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_authority_credential_assurances WHERE session_id=$1::uuid`, principal.Session.ID); n != 0 {
		t.Fatal("missing historical methods manufactured assurance")
	}
}
func TestPGPaymentPINNoDelegatedAssuranceAndRevocation(t *testing.T) {
	f := newPaymentPINFixture(t)
	_, p := f.bound(t)
	var child authority.Session
	var delegated authority.Pair
	e := authoritypg.New(f.db).WithinAccount(f.ctx, f.account.ID, func(tx authority.Transaction) error {
		var e error
		child, delegated, e = f.issuer.IssueOAuthTransaction(f.ctx, tx, p.SessionID, authorityUUID(t), []string{"openid"}, f.config.Now().Add(time.Hour))
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_authority_credential_assurances WHERE session_id=$1::uuid`, child.ID); n != 0 {
		t.Fatal("delegated assurance inherited")
	}
	if _, e = f.service.CreateFactor(f.ctx, delegated.Token, 4, syntheticPaymentPIN); e == nil {
		t.Fatal("OAuth bearer changed PIN")
	}
	if e = f.issuer.Revoke(f.ctx, f.pair.Token, "current"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN); e == nil {
		t.Fatal("revoked session consumed assurance")
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_authority_credential_assurances WHERE session_id=$1::uuid AND consumed_at IS NOT NULL`, p.SessionID); n != 0 {
		t.Fatal(n)
	}
	f.relogin(t)
	if e = f.issuer.InvalidateAccount(f.ctx, f.account.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.CreateFactor(f.ctx, f.pair.Token, 4, syntheticPaymentPIN); e == nil {
		t.Fatal("old account version consumed assurance")
	}
}

func TestPGPaymentPINPaymentProofOneUsePolicyAndPool(t *testing.T) {
	f := newPaymentPINFixture(t)
	factor := f.enroll(t)
	r, command := pinTransferFixture(t, f)
	ctx, p := f.bound(t)
	proof, e := f.pin.Atomic().PreparePayment(ctx, p, syntheticPaymentPIN, wallet.PaymentTransfer, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pin.Atomic().PreparePayment(ctx, p, syntheticPaymentPIN, "arbitrary.payment", command.Fingerprint); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("unknown purpose", e)
	}
	check, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pin.CheckPayment(proof, f.db, check, f.account.ID, wallet.PaymentOrder, command.Fingerprint); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("cross-purpose proof", e)
	}
	_ = check.Rollback()
	otherProvider := walletpg.NewPINVerifier(f.db, f.peppers).WithAuthority(f.guard).WithClock(f.config.Now)
	check, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = otherProvider.CheckPayment(proof, f.db, check, f.account.ID, wallet.PaymentTransfer, command.Fingerprint); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("foreign provider", e)
	}
	_ = check.Rollback()
	second := f.login(t, f.account.Name, syntheticPassword)
	principal, e := f.verifier.Authenticate(f.ctx, second.Token)
	if e != nil {
		t.Fatal(e)
	}
	check, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pin.CheckPayment(f.guard.Bind(proof, principal), f.db, check, f.account.ID, wallet.PaymentTransfer, command.Fingerprint); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("cross-session proof", e)
	}
	_ = check.Rollback()
	if _, e = r.Transfer(ctx, command); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("missing proof", e)
	}
	changed := command
	changed.Fingerprint = strings.Repeat("b", 64)
	if _, e = r.Transfer(proof, changed); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("binding substitution", e)
	}
	other := database(t)
	check, e = other.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pin.CheckPayment(proof, other, check, f.account.ID, wallet.PaymentTransfer, command.Fingerprint); !errors.Is(e, wallet.ErrUnavailable) {
		t.Fatal("foreign SQL pool", e)
	}
	_ = check.Rollback()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, e := r.Transfer(proof, command); done <- e }()
	}
	wins := 0
	for i := 0; i < 2; i++ {
		if e := <-done; e == nil {
			wins++
		} else if !errors.Is(e, wallet.ErrPIN) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal(wins)
	}
	walletAssertBalance(t, walletpg.New(f.db), command.PayerWalletID, command.Currency, "9.000", "0.000", "9.000")
	proof, e = f.pin.Atomic().PreparePayment(ctx, p, syntheticPaymentPIN, wallet.PaymentTransfer, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.service.DisableFactor(f.ctx, f.pair.Token, factor.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Transfer(proof, command); e == nil {
		t.Fatal("policy mutation retained authorization")
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NULL`, f.account.ID); n != 0 {
		t.Fatal(n)
	}
}
func TestPGPaymentPINLockWaitExpiryAndDurableFailures(t *testing.T) {
	f := newPaymentPINFixture(t)
	f.enroll(t)
	r, command := pinTransferFixture(t, f)
	ctx, p := f.bound(t)
	proof, e := f.pin.Atomic().PreparePayment(ctx, p, syntheticPaymentPIN, wallet.PaymentTransfer, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`SELECT id FROM wallet_v1.wallets WHERE id=$1::uuid FOR UPDATE`, command.PayerWalletID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Transfer(proof, command); done <- e }()
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: bounded}, pid, "wallets")
	f.now.Add(121)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("proof expired during wallet wait", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), command.PayerWalletID, command.Currency, "10.000", "0.000", "10.000")
	for i := 0; i < 5; i++ {
		if _, e = f.pin.Atomic().PreparePayment(ctx, p, "999999", wallet.PaymentTransfer, command.Fingerprint); !errors.Is(e, wallet.ErrPIN) {
			t.Fatal(e)
		}
	}
	reopened := walletpg.NewPINVerifier(database(t), f.peppers)
	if e = reopened.VerifyPayment(ctx, p, syntheticPaymentPIN); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("lockout not durable", e)
	}
	var attempts int
	var until sql.NullTime
	if e = f.db.QueryRow(`SELECT failed_attempts,locked_until FROM wallet_v1.payment_policies WHERE account_id=$1::uuid`, f.account.ID).Scan(&attempts, &until); e != nil || attempts != 5 || !until.Valid {
		t.Fatal(attempts, until, e)
	}
}
func TestPGPaymentPINCommercePolicyVersionAndAtomicSettlement(t *testing.T) {
	f := newCommerceFixture(t)
	pepper := sha256.Sum256([]byte("synthetic commerce PIN verifier"))
	p := walletpg.NewPINVerifier(f.db, map[string][]byte{"synthetic": pepper[:]}).WithAuthority(f.guard).WithClock(f.config.Now)
	if e := p.ProvisionPIN(f.ctx, f.actor, syntheticPaymentPIN, "synthetic"); e != nil {
		t.Fatal(e)
	}
	f.r.WithPaymentAuthorization(p)
	o := commerceOrder(t, f, "pin-order", false)
	principal := wallet.Principal{AccountID: f.actor, SessionID: f.principal.Session.ID}
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("missing order proof", e)
	}
	proof, e := p.Atomic().PreparePayment(f.ctx, principal, syntheticPaymentPIN, wallet.PaymentOrder, wallet.OrderPaymentBinding(o.ID, &f.wallet.ID))
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE wallet_v1.payment_policies SET version=version+1 WHERE account_id=$1::uuid`, f.actor)
	if _, e = f.r.PayOrder(proof, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("rotated policy accepted", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	proof, e = p.Atomic().PreparePayment(f.ctx, principal, syntheticPaymentPIN, wallet.PaymentOrder, wallet.OrderPaymentBinding(o.ID, &f.wallet.ID))
	if e != nil {
		t.Fatal(e)
	}
	other := walletCreate(t, walletpg.New(f.db), f.actor)
	if _, e = f.r.PayOrder(proof, f.actor, o.ID, &other.ID); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("payer substitution", e)
	}
	paid, e := f.r.PayOrder(proof, f.actor, o.ID, &f.wallet.ID)
	if e != nil || paid.Status != commerce.Paid {
		t.Fatal(paid, e)
	}
	if _, e = f.r.PayOrder(proof, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("proof replay", e)
	}
	proof, e = p.Atomic().PreparePayment(f.ctx, principal, syntheticPaymentPIN, wallet.PaymentOrder, wallet.OrderPaymentBinding(o.ID, &f.wallet.ID))
	if e != nil {
		t.Fatal(e)
	}
	replay, e := f.r.PayOrder(proof, f.actor, o.ID, &f.wallet.ID)
	if e != nil || replay.TransactionID == nil || paid.TransactionID == nil || *replay.TransactionID != *paid.TransactionID {
		t.Fatal(replay, e)
	}
	finished, e := f.r.UpdateOrder(f.ctx, o.ID, commerce.UpdateStatus{Credentials: f.credentials, Status: commerce.Finished})
	if e != nil || finished.Amount.Cmp(o.Amount) != 0 || finished.Currency != o.Currency || finished.PayeeWalletID != o.PayeeWalletID {
		t.Fatal("status update changed economics", finished, e)
	}
}
func TestPGPaymentPINMoneyRollbackAndPolicyLockOrdering(t *testing.T) {
	f := newPaymentPINFixture(t)
	factor := f.enroll(t)
	r, command := pinTransferFixture(t, f)
	ctx, p := f.bound(t)
	proof, e := f.pin.Atomic().PreparePayment(ctx, p, syntheticPaymentPIN, wallet.PaymentTransfer, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `CREATE FUNCTION synthetic_pin_money_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.account_id::text=TG_ARGV[0] AND NEW.consumed_at IS NOT NULL THEN RAISE EXCEPTION 'synthetic payment proof commit failure';END IF;RETURN NEW;END$$`)
	query(t, f.db, `CREATE TRIGGER synthetic_pin_money_fail BEFORE UPDATE ON wallet_v1.payment_authorizations FOR EACH ROW EXECUTE FUNCTION synthetic_pin_money_fail('`+f.account.ID+`')`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_pin_money_fail ON wallet_v1.payment_authorizations;DROP FUNCTION IF EXISTS synthetic_pin_money_fail()`)
	})
	if _, e = r.Transfer(proof, command); e == nil {
		t.Fatal("proof failure committed money")
	}
	walletAssertBalance(t, walletpg.New(f.db), command.PayerWalletID, command.Currency, "10.000", "0.000", "10.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.transactions WHERE actor_id=$1::uuid`, f.account.ID); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_pin_money_fail ON wallet_v1.payment_authorizations`)
	if _, e = r.Transfer(proof, command); e != nil {
		t.Fatal("rollback proof retry", e)
	}
	command.IdempotencyKey = "pin-policy-race"
	command.Fingerprint = strings.Repeat("c", 64)
	proof, e = f.pin.Atomic().PreparePayment(ctx, p, syntheticPaymentPIN, wallet.PaymentTransfer, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`SELECT account_id FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, f.account.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Transfer(proof, command); done <- e }()
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: bounded}, pid, "payment_policies")
	if _, e = tx.Exec(`UPDATE wallet_v1.payment_policies SET version=version+1 WHERE account_id=$1::uuid`, f.account.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, wallet.ErrPIN) {
		t.Fatal("rotation during policy wait", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), command.PayerWalletID, command.Currency, "9.000", "0.000", "9.000")
	proof, e = f.pin.Atomic().PreparePayment(ctx, p, syntheticPaymentPIN, wallet.PaymentTransfer, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	tx, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`SELECT id FROM wallet_v1.wallets WHERE id=$1::uuid FOR UPDATE`, command.PayerWalletID); e != nil {
		t.Fatal(e)
	}
	go func() { _, e := r.Transfer(proof, command); done <- e }()
	writer := waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: bounded}, pid, "wallets")
	mutation := make(chan error, 1)
	go func() { mutation <- f.service.DisableFactor(f.ctx, f.pair.Token, factor.ID) }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: bounded}, writer, "owned_authority_accounts")
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if e = <-mutation; e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), command.PayerWalletID, command.Currency, "8.000", "0.000", "8.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_policies WHERE account_id=$1::uuid AND disabled`, f.account.ID); n != 1 {
		t.Fatal(n)
	}
}
func TestPGBackendPaymentPINSourceHTTPComposition(t *testing.T) {
	f := newPaymentPINFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: authorityConfig(t, f.keys, f.now), SessionService: f.issuer, Signin: &f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, PINPeppers: f.peppers, PINEnrollmentPepperID: "synthetic-pin"})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	w := pinHTTP(t, app, "POST", "/stargate/factors", f.pair.Token, map[string]any{"type": 4, "secret": syntheticPaymentPIN}, 201)
	if strings.Contains(w.Body.String(), syntheticPaymentPIN) || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("PIN leaked")
	}
	factor := backendDecode[signin.Factor](t, w)
	if factor.Type != 4 {
		t.Fatal(factor)
	}
	f.relogin(t)
	status := backendDecode[wallet.PINStatus](t, backendCall(t, app, "GET", "/stargate/accounts/me/pin-status", f.pair.Token, "", 200))
	if !status.HasPIN || !status.ValidationRequired {
		t.Fatal(status)
	}
	backendCall(t, app, "POST", "/stargate/factors/"+factor.ID+"/disable", f.pair.Token, "", 204)
	f.relogin(t)
	pinHTTP(t, app, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, syntheticPaymentPIN, 200)
	f.relogin(t)
	backendCall(t, app, "GET", "/stargate/factors", f.pair.Token, "", 200)
}
func TestPGPaymentPINRestartPersistence(t *testing.T) {
	db := database(t)
	file := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "payment-pin-record.json")
	type saved struct{ Actor, Factor string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newPaymentPINFixture(t)
		factor := f.enroll(t)
		_, p := f.bound(t)
		if e := f.pin.VerifyPayment(f.ctx, p, "999999"); !errors.Is(e, wallet.ErrPIN) {
			t.Fatal(e)
		}
		b, _ := json.Marshal(saved{f.account.ID, factor.ID})
		if e := os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var data saved
	if e = json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	var linked string
	var failures, consumed int
	var secret []byte
	if e = db.QueryRow(`SELECT p.factor_id::text,p.failed_attempts,f.secret,(SELECT count(*)FROM owned_authority_credential_assurances a WHERE a.account_id=p.account_id AND a.consumed_at IS NOT NULL)FROM wallet_v1.payment_policies p JOIN owned_authority_factors f ON f.id=p.factor_id WHERE p.account_id=$1::uuid`, data.Actor).Scan(&linked, &failures, &secret, &consumed); e != nil || linked != data.Factor || failures != 1 || secret != nil || consumed != 1 {
		t.Fatal(linked, failures, consumed, e)
	}
	pepper := sha256.Sum256([]byte("owned synthetic disposable payment PIN pepper"))
	verifier := walletpg.NewPINVerifier(db, map[string][]byte{"synthetic-pin": pepper[:]})
	if e = verifier.VerifyPayment(context.Background(), wallet.Principal{AccountID: data.Actor, SessionID: "synthetic-restart-verification"}, syntheticPaymentPIN); e != nil {
		t.Fatal(e)
	}
}
func TestPGPaymentPINDisabledPolicyBlocksStandingRenewal(t *testing.T) {
	f := newCommerceFixture(t)
	pepper := sha256.Sum256([]byte("synthetic renewal policy pepper"))
	pin := walletpg.NewPINVerifier(f.db, map[string][]byte{"synthetic": pepper[:]}).WithAuthority(f.guard).WithClock(f.config.Now)
	if e := pin.ProvisionPIN(f.ctx, f.actor, syntheticPaymentPIN, "synthetic"); e != nil {
		t.Fatal(e)
	}
	f.r.WithPaymentAuthorization(pin)
	s, e := f.r.CreateSubscription(f.ctx, f.actor, commerce.CreateSubscription{Identifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: commerce.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "pin-renewal", false)
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, f.plan)
	if e != nil {
		t.Fatal(e)
	}
	p := wallet.Principal{AccountID: f.actor, SessionID: f.principal.Session.ID}
	ctx, e := pin.Atomic().PreparePayment(f.ctx, p, syntheticPaymentPIN, wallet.PaymentOrder, wallet.OrderPaymentBinding(o.ID, &f.wallet.ID))
	if e != nil {
		t.Fatal(e)
	}
	paid, e := f.r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID)
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.r.Subscription(f.ctx, f.actor, f.plan)
	if e != nil {
		t.Fatal(e)
	}
	past := time.Unix(f.clock.Load(), 0).Add(-time.Second)
	s.EndedAt = past
	s.RenewalAt = &past
	b, _ := json.Marshal(s)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2::timestamptz,renewal_at=$2::timestamptz,document=$3::jsonb WHERE id=$1::uuid`, s.ID, past, b)
	tx, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_authority_accounts", f.actor)
	done := make(chan error, 1)
	go func() { _, e := f.r.ProcessDue(f.ctx, 1000); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
	if _, e = tx.Exec(`UPDATE wallet_v1.payment_policies SET disabled=true,version=version+1 WHERE account_id=$1::uuid`, f.actor); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.667", "0.000", "96.667")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM commerce_v1.outbox WHERE aggregate_id=$1::uuid AND event_type='commerce.subscription.renewed'`, s.ID); n != 0 {
		t.Fatal("disabled renewal charged")
	}
	current, e := f.r.GetOrder(f.ctx, f.actor, o.ID, false)
	if e != nil || current.TransactionID == nil || paid.TransactionID == nil || *current.TransactionID != *paid.TransactionID || current.Amount.Cmp(paid.Amount) != 0 {
		t.Fatal("settled history changed", current, e)
	}
}

func TestPGPaymentPINAssuranceExpiryDuringPolicyWait(t *testing.T) {
	f := newPaymentPINFixture(t)
	factor := f.enroll(t)
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`SELECT account_id FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, f.account.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- f.service.DisableFactor(f.ctx, f.pair.Token, factor.ID) }()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "payment_policies")
	f.now.Add(301)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, signin.ErrReauthentication) {
		t.Fatal("late assurance expiry did not request ordinary login", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_policies p JOIN owned_authority_factors f ON f.id=p.factor_id WHERE p.account_id=$1::uuid AND NOT p.disabled AND f.enabled_at IS NOT NULL`, f.account.ID); n != 1 {
		t.Fatal("expired assurance partially disabled PIN")
	}
	f.relogin(t)
	if e = f.service.DisableFactor(f.ctx, f.pair.Token, factor.ID); e != nil {
		t.Fatal("ordinary login did not restore retry", e)
	}
}
