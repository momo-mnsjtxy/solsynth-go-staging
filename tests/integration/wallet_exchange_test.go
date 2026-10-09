// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	w "solsynth.local/go-migration/ports/wallet/accounts"
)

type exchangeFixture struct {
	backendFixture
	payer, treasury w.Wallet
	policy          *w.ExchangePolicy
	now             *atomic.Int64
	guard           *authoritypg.TransactionGuard
	principal       authority.Principal
	ctx             context.Context
	pin             *walletpg.PINVerifier
	repo            *walletpg.Repository
}

func exchangeBackend(t *testing.T, db *sql.DB, config authority.Config, policy w.ExchangeConfig, peppers map[string][]byte) *application.Backend {
	t.Helper()
	visibility := int32(0)
	rooms := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(db, nil, application.Config{Authority: config, PublicVisibility: &visibility, MessagerPolicy: &rooms, WalletExchange: &policy, PINPeppers: peppers})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return app
}
func newExchangeFixture(t *testing.T) exchangeFixture {
	t.Helper()
	return newExchangeFixturePolicy(t, false)
}
func newExchangeFixturePolicy(t *testing.T, persist bool) exchangeFixture {
	t.Helper()
	if os.Getenv("PG_TEST_PHASE") == "after" {
		t.Fatal("exchange fixture setup is forbidden after restart")
	}
	f := exchangeFixture{backendFixture: newBackendFixture(t), now: &atomic.Int64{}}
	f.now.Store(f.config.Now().Unix())
	f.config.Now = func() time.Time { return time.Unix(f.now.Load(), 123456789).UTC() }
	f.payer, f.treasury = reportingWallets(t, f.backendFixture)
	policy, e := w.NewExchangePolicy(w.ExchangeConfig{Version: "synthetic-" + authorityUUID(t), TreasuryAccountID: f.other, TreasuryWalletID: f.treasury.ID, Rates: []w.ExchangeRate{{SourceCurrency: "points", SourceAmount: "3", TargetCurrency: "golds", TargetAmount: "1"}}})
	if e != nil {
		t.Fatal(e)
	}
	f.policy = policy
	if !persist {
		// Before-phase tests run sequentially in each disposable database. Keep
		// immutable history, and restore only the exact pre-existing active tuple.
		// This also preserves a restart target created earlier in the same group.
		var previousVersion string
		var previousEnabled bool
		e := f.db.QueryRow(`SELECT version,enabled FROM wallet_v1.exchange_active_policy WHERE singleton`).Scan(&previousVersion, &previousEnabled)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			t.Fatal(e)
		}
		previousExists := e == nil
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var e error
			if previousExists {
				_, e = f.db.ExecContext(ctx, `INSERT INTO wallet_v1.exchange_active_policy(singleton,version,enabled)VALUES(true,$1,$2)ON CONFLICT(singleton)DO UPDATE SET version=EXCLUDED.version,enabled=EXCLUDED.enabled`, previousVersion, previousEnabled)
			} else {
				_, e = f.db.ExecContext(ctx, `DELETE FROM wallet_v1.exchange_active_policy WHERE singleton`)
			}
			if e != nil {
				t.Errorf("restore before-phase synthetic exchange policy: %v", e)
			}
		})
	}
	query(t, f.db, `INSERT INTO wallet_v1.exchange_policy_versions(version,config_hash,created_at)VALUES($1,$2,now())`, policy.Config().Version, policy.Digest())
	query(t, f.db, `INSERT INTO wallet_v1.exchange_active_policy(singleton,version,enabled)VALUES(true,$1,true)ON CONFLICT(singleton)DO UPDATE SET version=EXCLUDED.version,enabled=true`, policy.Config().Version)
	f.app = exchangeBackend(t, f.db, f.config, policy.Config(), nil)
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.principal, e = verifier.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	f.ctx = f.guard.Bind(ctx, f.principal)
	f.pin = walletpg.NewPINVerifier(f.db, nil).WithAuthority(f.guard).WithClock(f.config.Now)
	f.repo = walletpg.NewWithAuthority(f.db, f.guard).WithPaymentAuthorization(f.pin).WithExchangePolicy(policy).WithClock(f.config.Now)
	return f
}
func exchangeCall(f exchangeFixture, token, key, amount, pin string) *httptest.ResponseRecorder {
	body := map[string]any{"currency": "points", "amount": json.Number(amount), "wallet_id": f.payer.ID}
	if pin != "" {
		body["pin_code"] = pin
	}
	raw, _ := json.Marshal(body)
	req := fixtureRawJSONRequest("POST", "/wallet/wallets/exchange", token, string(raw))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	out := httptest.NewRecorder()
	f.app.ServeHTTP(out, req)
	return out
}
func exchangeCommand(t *testing.T, f exchangeFixture, key, amount string) (context.Context, w.ExchangeCommand) {
	t.Helper()
	quote, e := f.repo.QuoteExchange("points", walletAmount(t, amount))
	if e != nil {
		t.Fatal(e)
	}
	c := w.ExchangeCommand{ActorID: f.actor, WalletID: f.payer.ID, Owner: f.payer.Owner(), Quote: quote, IdempotencyKey: key}
	c.Fingerprint = w.ExchangeBinding(c)
	proof, e := f.pin.Atomic().PreparePayment(f.ctx, w.Principal{AccountID: f.actor, SessionID: f.principal.Session.ID}, "", w.PaymentExchange, c.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	return proof, c
}
func startExchange(t *testing.T, f exchangeFixture, proof context.Context, c w.ExchangeCommand) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(proof)
	done, stopped := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(stopped)
		_, e := f.repo.Exchange(ctx, c)
		done <- e
	}()
	// Runs before fixture policy restoration, including a failed assertion.
	// Never restore the singleton while a transaction is still using it.
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return done
}
func assertExchangeConservation(t *testing.T, f exchangeFixture) {
	t.Helper()
	for _, currency := range []string{"points", "golds"} {
		var amount string
		if e := f.db.QueryRow(`SELECT sum(amount+held_amount)::text FROM wallet_v1.pockets WHERE wallet_id IN($1,$2)AND currency=$3`, f.payer.ID, f.treasury.ID, currency).Scan(&amount); e != nil || amount != "200.000" {
			t.Fatal(currency, amount, e)
		}
	}
}
func TestPGBackendWalletExchangePINReplayAndConservation(t *testing.T) {
	f := newExchangeFixture(t)
	catalog := backendCall(t, f.app, "GET", "/wallet/wallets/exchange", "", "", 200)
	var options []struct {
		SourceAmount json.Number `json:"source_amount"`
		TargetAmount json.Number `json:"target_amount"`
	}
	if e := json.Unmarshal(catalog.Body.Bytes(), &options); e != nil || len(options) != 1 || options[0].SourceAmount.String() != "3" || options[0].TargetAmount.String() != "1" {
		t.Fatal(catalog.Body.String(), e)
	}
	if out := exchangeCall(f, f.token, "", "1.0019", ""); out.Code != 400 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := exchangeCall(f, "", "synthetic", "1.0019", ""); out.Code != 401 {
		t.Fatal(out.Code)
	}
	if out := exchangeCall(f, f.otherToken, "synthetic", "1.0019", ""); out.Code != 403 {
		t.Fatal(out.Code, out.Body.String())
	}
	peppers := map[string][]byte{"synthetic": make([]byte, 32)}
	if e := walletpg.NewPINVerifier(f.db, peppers).ProvisionPIN(f.ctx, f.actor, syntheticPaymentPIN, "synthetic"); e != nil {
		t.Fatal(e)
	}
	f.app = exchangeBackend(t, f.db, f.config, f.policy.Config(), peppers)
	for _, pin := range []string{"", "999999"} {
		out := exchangeCall(f, f.token, "synthetic", "1.0019", pin)
		if out.Code != 403 {
			t.Fatal("PIN bypass", out.Code, out.Body.String())
		}
	}
	out := exchangeCall(f, f.token, "synthetic", "1.0019", syntheticPaymentPIN)
	v := backendDecode[w.ExchangeResponse](t, expectExchangeStatus(t, out, 200))
	if v.SourceAmount.String() != "1.001" || v.TargetAmount.String() != "0.333" || v.RemainderMills != "2/3" || v.DebitTransaction.PayerWalletID != f.payer.ID || v.CreditTransaction.PayerWalletID != f.treasury.ID || v.DebitTransaction.Status != w.Confirmed || v.CreditTransaction.Status != w.Confirmed {
		t.Fatal(v)
	}
	for _, secret := range []string{syntheticPaymentPIN, "pin_mac", "policy_hash", "fingerprint", "idempotency_key"} {
		if strings.Contains(out.Body.String(), secret) {
			t.Fatal("private proof leaked", secret)
		}
	}
	replayed := expectExchangeStatus(t, exchangeCall(f, f.token, "synthetic", "1.0019", syntheticPaymentPIN), 200)
	if !bytes.Equal(out.Body.Bytes(), replayed.Body.Bytes()) {
		t.Fatalf("immutable replay changed response: first=%s replay=%s", out.Body.String(), replayed.Body.String())
	}
	repeat := backendDecode[w.ExchangeResponse](t, replayed)
	if repeat.DebitTransaction.ID != v.DebitTransaction.ID || repeat.CreditTransaction.ID != v.CreditTransaction.ID {
		t.Fatal("replay minted new legs")
	}
	expectExchangeStatus(t, exchangeCall(f, f.token, "synthetic", "2.001", syntheticPaymentPIN), 409)
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "98.999", "0.000", "98.999")
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "golds", "100.333", "0.000", "100.333")
	assertExchangeConservation(t, f)
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.transactions WHERE actor_id=$1 AND purpose='exchange'`, f.actor); n != 2 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.exchanges WHERE actor_id=$1`, f.actor); n != 1 {
		t.Fatal(n)
	}
	var storedQuote []byte
	if e := f.db.QueryRow(`SELECT quote FROM wallet_v1.exchanges WHERE actor_id=$1 AND idempotency_key='synthetic'`, f.actor).Scan(&storedQuote); e != nil {
		t.Fatal(e)
	}
	var original w.ExchangeQuote
	if e := json.Unmarshal(storedQuote, &original); e != nil || original.PolicyHash != f.policy.Digest() || original.RemainderMills != "2/3" {
		t.Fatal(original, e)
	}
	next := f.policy.Config()
	next.Version += "-next"
	nextPolicy, e := w.NewExchangePolicy(next)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO wallet_v1.exchange_policy_versions(version,config_hash,created_at)VALUES($1,$2,now())`, next.Version, nextPolicy.Digest())
	query(t, f.db, `UPDATE wallet_v1.exchange_active_policy SET version=$1 WHERE singleton`, next.Version)
	f.app = exchangeBackend(t, f.db, f.config, next, peppers)
	expectExchangeStatus(t, exchangeCall(f, f.token, "synthetic", "1.0019", syntheticPaymentPIN), 409)
	if _, e = f.db.Exec(`UPDATE wallet_v1.exchanges SET policy_version=$2 WHERE actor_id=$1`, f.actor, next.Version); e == nil {
		t.Fatal("committed replay policy changed")
	}
	// Normal lifecycle actions cannot individually refund/confirm an exchange leg.
	backendCall(t, f.app, "POST", "/api/wallets/transactions/"+v.CreditTransaction.ID+"/reject", f.token, "", 404)
	restricted, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Scopes: []string{"accounts.profile.read"}})
	if e != nil {
		t.Fatal(e)
	}
	expectExchangeStatus(t, exchangeCall(f, restricted.Token, "scope-denied", "1", syntheticPaymentPIN), 403)
}
func expectExchangeStatus(t *testing.T, out *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
	t.Helper()
	if out.Code != status {
		t.Fatalf("exchange status=%d body=%s", out.Code, out.Body.String())
	}
	return out
}

func TestPGWalletExchangeAtomicRollbackAndConcurrentLedgerIsolation(t *testing.T) {
	f := newExchangeFixture(t)
	proof, command := exchangeCommand(t, f, "rollback", "3.003")
	query(t, f.db, `CREATE FUNCTION synthetic_exchange_receipt_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.actor_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic receipt failure';END IF;RETURN NEW;END$$`)
	query(t, f.db, `CREATE TRIGGER synthetic_exchange_receipt_fail BEFORE INSERT ON wallet_v1.exchanges FOR EACH ROW EXECUTE FUNCTION synthetic_exchange_receipt_fail('`+f.actor+`')`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_exchange_receipt_fail ON wallet_v1.exchanges;DROP FUNCTION IF EXISTS synthetic_exchange_receipt_fail()`)
	})
	if _, e := f.repo.Exchange(proof, command); e == nil {
		t.Fatal("injected second-leg receipt failure committed")
	}
	assertExchangeConservation(t, f)
	walletAssertBalance(t, f.repo, f.payer.ID, "points", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.transactions WHERE actor_id=$1`, f.actor); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1 AND consumed_at IS NOT NULL`, f.actor); n != 0 {
		t.Fatal("failed exchange consumed proof", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.outbox WHERE event_type='wallet.transaction.created' AND(payload->>'payer_wallet_id'=$1 OR payload->>'payee_wallet_id'=$1)`, f.payer.ID); n != 0 {
		t.Fatal("rolled-back exchange emitted events", n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_exchange_receipt_fail ON wallet_v1.exchanges`)
	if _, e := f.repo.Exchange(proof, command); e != nil {
		t.Fatal("rolled-back original proof not reusable", e)
	}
	// Reusing a consumed payment proof cannot even replay a committed receipt.
	if _, e := f.repo.Exchange(proof, command); !errors.Is(e, w.ErrPIN) {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- exchangeCall(f, f.token, "concurrent-replay", "1.001", "") }()
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	for result := range results {
		v := backendDecode[w.ExchangeResponse](t, expectExchangeStatus(t, result, 200))
		ids[v.DebitTransaction.ID] = true
	}
	if len(ids) != 1 {
		t.Fatal(ids)
	}
	assertExchangeConservation(t, f)
	// A canonical transfer and an exchange compete for the same available pocket.
	results = make(chan *httptest.ResponseRecorder, 2)
	wg.Add(2)
	go func() { defer wg.Done(); results <- exchangeCall(f, f.token, "overspend-exchange", "80", "") }()
	go func() {
		defer wg.Done()
		body := `{"amount":30,"currency":"points","payer_wallet_id":"` + f.payer.ID + `","payee_wallet_id":"` + f.treasury.ID + `"}`
		req := fixtureRawJSONRequest("POST", "/api/wallets/transfer", f.token, body)
		req.Header.Set("Idempotency-Key", "competing-transfer")
		out := httptest.NewRecorder()
		f.app.ServeHTTP(out, req)
		results <- out
	}()
	wg.Wait()
	close(results)
	success, insufficient := 0, 0
	for result := range results {
		switch result.Code {
		case 200:
			success++
		case 400:
			insufficient++
		default:
			t.Fatal(result.Code, result.Body.String())
		}
	}
	if success != 1 || insufficient != 1 {
		t.Fatal(success, insufficient)
	}
	assertExchangeConservation(t, f)
}

func TestPGWalletExchangeCurrentPolicyAndExpiredPaymentProof(t *testing.T) {
	f := newExchangeFixture(t)
	proof, c := exchangeCommand(t, f, "policy-race", "3")
	blocker, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var pid int
	if e = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()FROM wallet_v1.exchange_active_policy WHERE singleton FOR UPDATE`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	done := startExchange(t, f, proof, c)
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "exchange_active_policy")
	if _, e = blocker.ExecContext(f.ctx, `UPDATE wallet_v1.exchange_active_policy SET enabled=false WHERE singleton`); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, w.ErrUnavailable) {
		t.Fatal(e)
	}
	walletAssertBalance(t, f.repo, f.payer.ID, "points", "100.000", "0.000", "100.000")
	query(t, f.db, `UPDATE wallet_v1.exchange_active_policy SET enabled=true WHERE singleton`)
	proof, c = exchangeCommand(t, f, "expiry-race", "3")
	blocker, e = f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if e = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()FROM wallet_v1.wallets WHERE id=$1 FOR UPDATE`, f.payer.ID).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	done = startExchange(t, f, proof, c)
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "wallets")
	f.now.Add(121)
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, w.ErrPIN) {
		t.Fatal("expired proof changed money", e)
	}
	assertExchangeConservation(t, f)
	walletAssertBalance(t, f.repo, f.payer.ID, "points", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.exchanges WHERE actor_id=$1`, f.actor); n != 0 {
		t.Fatal(n)
	}
	// A differently configured replica with the same version fails closed.
	changed := f.policy.Config()
	changed.Rates[0].TargetAmount = "2"
	different, e := w.NewExchangePolicy(changed)
	if e != nil {
		t.Fatal(e)
	}
	stale := walletpg.NewWithAuthority(f.db, f.guard).WithPaymentAuthorization(f.pin).WithExchangePolicy(different)
	if e = stale.ExchangeReady(f.ctx); !errors.Is(e, w.ErrUnavailable) {
		t.Fatal("same-version rate drift accepted", e)
	}
	if _, e = f.db.Exec(`UPDATE wallet_v1.exchange_policy_versions SET config_hash=$2 WHERE version=$1`, changed.Version, different.Digest()); e == nil {
		t.Fatal("immutable rate version was replaced")
	}
	t.Run("treasury-disablement-starts-during-wallet-wait", func(t *testing.T) {
		f := newExchangeFixture(t)
		starts := f.config.Now().Add(time.Second)
		if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.other, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account", StartsAt: &starts}}); e != nil {
			t.Fatal(e)
		}
		if e := f.repo.ExchangeReady(f.ctx); e != nil {
			t.Fatal("future treasury punishment applied early", e)
		}
		proof, command := exchangeCommand(t, f, "treasury-expiry-race", "3")
		blocker, e := f.db.BeginTx(f.ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer blocker.Rollback()
		var pid int
		if e = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()FROM wallet_v1.wallets WHERE id=$1 FOR UPDATE`, f.payer.ID).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		done := startExchange(t, f, proof, command)
		waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "wallets")
		f.now.Add(2)
		if e = blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e = <-done; !errors.Is(e, w.ErrForbidden) {
			t.Fatal("newly disabled treasury changed money", e)
		}
		if e = f.repo.ExchangeReady(f.ctx); !errors.Is(e, w.ErrUnavailable) {
			t.Fatal("punished treasury remained ready", e)
		}
		assertExchangeConservation(t, f)
		walletAssertBalance(t, f.repo, f.payer.ID, "points", "100.000", "0.000", "100.000")
		walletAssertBalance(t, f.repo, f.payer.ID, "golds", "100.000", "0.000", "100.000")
		if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.exchanges WHERE actor_id=$1`, f.actor); n != 0 {
			t.Fatal("disabled treasury retained a receipt", n)
		}
		if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1 AND consumed_at IS NOT NULL`, f.actor); n != 0 {
			t.Fatal("disabled treasury consumed payment proof", n)
		}
	})
}

func TestPGWalletExchangeRealmGrantExpiresDuringWalletWait(t *testing.T) {
	f := newExchangeFixture(t)
	realm := authorityUUID(t)
	rw, e := walletpg.New(f.db).CreateWallet(f.ctx, w.Owner{RealmID: realm}, "Synthetic exchange realm wallet")
	if e != nil {
		t.Fatal(e)
	}
	walletFund(t, f.db, rw, "points", "100")
	var expires time.Time
	if e = f.db.QueryRow(`INSERT INTO wallet_v1.realm_grants(account_id,realm_id,role,expires_at,updated_at)VALUES($1,$2,'editor',clock_timestamp()+interval '2 seconds',now()) RETURNING expires_at`, f.actor, realm).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	quote, e := f.repo.QuoteExchange("points", walletAmount(t, "3"))
	if e != nil {
		t.Fatal(e)
	}
	c := w.ExchangeCommand{ActorID: f.actor, WalletID: rw.ID, Owner: rw.Owner(), Quote: quote, IdempotencyKey: "realm-expiry"}
	c.Fingerprint = w.ExchangeBinding(c)
	proof, e := f.pin.Atomic().PreparePayment(f.ctx, w.Principal{AccountID: f.actor, SessionID: f.principal.Session.ID}, "", w.PaymentExchange, c.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	blocker, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var pid int
	if e = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid() FROM wallet_v1.wallets WHERE id=$1 FOR UPDATE`, rw.ID).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	done := startExchange(t, f, proof, c)
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "wallets")
	if remaining := time.Until(expires.Add(20 * time.Millisecond)); remaining > 0 {
		time.Sleep(remaining)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, w.ErrForbidden) {
		t.Fatal("expired realm grant committed an exchange", e)
	}
	walletAssertBalance(t, f.repo, rw.ID, "points", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.exchanges WHERE actor_id=$1`, f.actor); n != 0 {
		t.Fatal(n)
	}
}

func TestPGBackendWalletExchangeDisabledAndTreasuryLiquidity(t *testing.T) {
	f := newExchangeFixture(t)
	defaultApp := backendApp(t, f.db, f.config)
	out := httptest.NewRecorder()
	req := fixtureRawJSONRequest("POST", "/api/wallets/exchange", f.token, `{"amount":3,"currency":"points","wallet_id":"`+f.payer.ID+`"}`)
	req.Header.Set("Idempotency-Key", "disabled")
	defaultApp.ServeHTTP(out, req)
	expectExchangeStatus(t, out, 503)
	held := reportingTransfer(t, f.backendFixture, f.payer, f.treasury, "90", "points", f.token, true)
	expectExchangeStatus(t, exchangeCall(f, f.token, "cannot-spend-hold", "30", ""), 400)
	walletAssertBalance(t, f.repo, f.payer.ID, "points", "10.000", "90.000", "10.000")
	backendCall(t, f.app, "POST", "/api/wallets/transactions/"+held.ID+"/reject", f.otherToken, "", 200)
	// Only synthetic fixture funding is changed; no treasury provisioning API exists.
	query(t, f.db, `UPDATE wallet_v1.pockets SET amount=0 WHERE wallet_id=$1 AND currency='golds'`, f.treasury.ID)
	expectExchangeStatus(t, exchangeCall(f, f.token, "no-liquidity", "3", ""), 400)
	walletAssertBalance(t, f.repo, f.payer.ID, "points", "100.000", "0.000", "100.000")
	walletAssertBalance(t, f.repo, f.payer.ID, "golds", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.transactions WHERE actor_id=$1 AND purpose='exchange'`, f.actor); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.other)
	expectExchangeStatus(t, exchangeCall(f, f.token, "disabled-treasury", "3", ""), 403)
	for _, leg := range []string{"source-credit", "target-credit"} {
		t.Run("overflow-rolls-back/"+leg, func(t *testing.T) {
			f := newExchangeFixture(t)
			wallet, currency := f.treasury.ID, "points"
			if leg == "target-credit" {
				wallet, currency = f.payer.ID, "golds"
			}
			const maximum = "79228162514264337593543950335.000"
			query(t, f.db, `UPDATE wallet_v1.pockets SET amount=$3 WHERE wallet_id=$1 AND currency=$2`, wallet, currency, maximum)
			proof, command := exchangeCommand(t, f, "overflow", "3")
			if _, e := f.repo.Exchange(proof, command); e == nil {
				t.Fatal("overflow committed")
			}
			walletAssertBalance(t, f.repo, wallet, currency, maximum, "0.000", maximum)
			walletAssertBalance(t, f.repo, f.payer.ID, "points", "100.000", "0.000", "100.000")
			walletAssertBalance(t, f.repo, f.treasury.ID, "golds", "100.000", "0.000", "100.000")
			if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.transactions WHERE actor_id=$1 AND purpose='exchange'`, f.actor); n != 0 {
				t.Fatal("overflow retained ledger legs", n)
			}
			if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1 AND consumed_at IS NOT NULL`, f.actor); n != 0 {
				t.Fatal("overflow consumed proof", n)
			}
		})
	}
}

func TestPGBackendWalletExchangeOriginalProofRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "wallet-exchange-original-proof.json"
	type saved struct {
		Actor, Token, Wallet, Debit, Credit, Version string
		Authority                                    restartAuthoritySnapshot
		Policy                                       w.ExchangeConfig
		Response                                     string
	}
	if phase == "before" {
		f := newExchangeFixturePolicy(t, true)
		now, wall := f.config.Now(), time.Now()
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f.config.Now = func() time.Time { return now.Add(time.Since(wall)) }
		issuer, e := authority.New(authoritypg.New(db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.token = pgIssue(t, issuer, f.actor).Token
		f.app = exchangeBackend(t, db, f.config, f.policy.Config(), nil)
		response := expectExchangeStatus(t, exchangeCall(f, f.token, "restart-exchange", "1.001", ""), 200)
		result := backendDecode[w.ExchangeResponse](t, response)
		saveRestartFixture(t, marker, saved{f.actor, f.token, f.payer.ID, result.DebitTransaction.ID, result.CreditTransaction.ID, result.PolicyVersion, saveAuthoritySnapshot(t, f.config), f.policy.Config(), string(bytes.TrimSpace(response.Body.Bytes()))})
		// A later ordinary fixture must not invalidate this already-saved marker.
		// This is entirely before restart; the after branch performs no setup.
		t.Run("later-fixture-preserves-original-policy", func(t *testing.T) {
			later := newExchangeFixture(t)
			if e := f.repo.ExchangeReady(f.ctx); !errors.Is(e, w.ErrUnavailable) {
				t.Fatal("temporary policy failed to supersede original", e)
			}
			query(t, later.db, `UPDATE wallet_v1.exchange_active_policy SET enabled=false WHERE singleton`)
		})
		if e := f.repo.ExchangeReady(f.ctx); e != nil {
			t.Fatal("later fixture invalidated saved restart policy", e)
		}
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	config := restoreAuthoritySnapshot(t, v.Authority)
	app := exchangeBackend(t, db, config, v.Policy, nil)
	f := exchangeFixture{backendFixture: backendFixture{db: db, app: app, actor: v.Actor, token: v.Token}, payer: w.Wallet{ID: v.Wallet}}
	response := expectExchangeStatus(t, exchangeCall(f, v.Token, "restart-exchange", "1.001", ""), 200)
	if !bytes.Equal(bytes.TrimSpace(response.Body.Bytes()), []byte(v.Response)) {
		t.Fatal("restart changed the original immutable exchange response")
	}
	result := backendDecode[w.ExchangeResponse](t, response)
	if result.DebitTransaction.ID != v.Debit || result.CreditTransaction.ID != v.Credit || result.PolicyVersion != v.Version || result.TargetAmount.String() != "0.333" || result.RemainderMills != "2/3" {
		t.Fatal(result)
	}
	walletAssertBalance(t, walletpg.New(db), v.Wallet, "points", "98.999", "0.000", "98.999")
	walletAssertBalance(t, walletpg.New(db), v.Wallet, "golds", "100.333", "0.000", "100.333")
	if n := commerceCount(t, db, `SELECT count(*)FROM wallet_v1.exchanges WHERE actor_id=$1`, v.Actor); n != 1 {
		t.Fatal(n)
	}
}
