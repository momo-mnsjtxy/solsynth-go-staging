// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	w "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type requestFixture struct {
	backendFixture
	payer, payee w.Wallet
}

func newRequestFixture(t *testing.T) requestFixture {
	t.Helper()
	f := requestFixture{backendFixture: newBackendFixture(t)}
	repo := walletpg.New(f.db)
	f.payer = walletCreate(t, repo, f.actor)
	f.payee = walletCreate(t, repo, f.other)
	walletFund(t, f.db, f.payer, "synthetic-request", "100.000")
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1::uuid,false,$2::timestamptz)`, f.actor, f.config.Now())
	f.payee = backendDecode[w.Wallet](t, backendCall(t, f.app, "POST", "/wallet/wallets/"+f.payee.ID+"/public-id/enable", f.otherToken, "", 200))
	return f
}
func (f requestFixture) create(t *testing.T) w.TransferRequestRecord {
	t.Helper()
	return backendDecode[w.TransferRequestRecord](t, backendJSONCall(t, f.app, "POST", "/wallet/wallets/transfer/requests", f.otherToken, map[string]any{"amount": 10, "currency": "synthetic-request", "wallet_id": f.payee.ID, "remark": "  Synthetic request  "}, 200))
}
func (f requestFixture) pay(t *testing.T, id string, status int) *httptest.ResponseRecorder {
	t.Helper()
	return backendJSONCall(t, f.app, "POST", "/wallet/wallets/transfer", f.token, map[string]any{"transfer_request_id": id, "payer_wallet_id": f.payer.ID, "amount": 999, "currency": "ignored-untrusted-input", "payee_wallet_id": f.payer.ID, "freeze": true}, status)
}
func TestPGWalletTransferRequestSourceHTTPAndAtomicFulfillment(t *testing.T) {
	f := newRequestFixture(t)
	q := f.create(t)
	if q.Amount.String() != "10.000" || q.Remark == nil || *q.Remark != "Synthetic request" || q.PayeePublicID == nil || q.Freeze {
		t.Fatal(q)
	}
	got := backendDecode[w.TransferRequestRecord](t, backendCall(t, f.app, "GET", "/api/wallets/transfer/requests/"+q.ID, "", "", 200))
	if got.ID != q.ID || got.PayeeWalletID != f.payee.ID {
		t.Fatal(got)
	}
	paid := backendDecode[w.Transaction](t, f.pay(t, q.ID, 200))
	if paid.Amount.String() != "10.000" || paid.Currency != "synthetic-request" || paid.PayeeWalletID != f.payee.ID || paid.IsFrozen {
		t.Fatal("caller overwrote immutable request", paid)
	}
	again := backendDecode[w.Transaction](t, f.pay(t, q.ID, 200))
	if again.ID != paid.ID {
		t.Fatal("retry duplicated transfer")
	}
	backendCall(t, f.app, "GET", "/wallet/wallets/transfer/requests/"+q.ID, "", "", 404)
	repo := walletpg.New(f.db)
	walletAssertBalance(t, repo, f.payer.ID, "synthetic-request", "90.000", "0.000", "90.000")
	walletAssertBalance(t, repo, f.payee.ID, "synthetic-request", "10.000", "0.000", "10.000")
	var linked string
	if e := f.db.QueryRow(`SELECT transaction_id::text FROM wallet_v1.transfer_requests WHERE id=$1::uuid AND status=1`, q.ID).Scan(&linked); e != nil || linked != paid.ID {
		t.Fatal(linked, e)
	}
}
func TestPGWalletTransferRequestConcurrentPayersAndReplay(t *testing.T) {
	f := newRequestFixture(t)
	q := f.create(t)
	third := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(context.Background(), f.db, third, "synthetic_request_"+strings.ReplaceAll(third, "-", ""), f.config.Now()); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), third, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, f.issuer, third).Token
	otherWallet := walletCreate(t, walletpg.New(f.db), third)
	walletFund(t, f.db, otherWallet, "synthetic-request", "100.000")
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1::uuid,false,$2::timestamptz)`, third, f.config.Now())
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	start := make(chan struct{})
	for _, payer := range []struct{ token, id string }{{f.token, f.payer.ID}, {token, otherWallet.ID}} {
		raw, _ := json.Marshal(map[string]any{"transfer_request_id": q.ID, "payer_wallet_id": payer.id})
		wg.Add(1)
		go func(token, body string) {
			defer wg.Done()
			<-start
			r := fixtureRawJSONRequest("POST", "/wallet/wallets/transfer", token, body)
			v := httptest.NewRecorder()
			f.app.ServeHTTP(v, r)
			codes <- v.Code
		}(payer.token, string(raw))
	}
	close(start)
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code != 404 && code != 409 {
			t.Fatal("unexpected race result", code)
		}
	}
	if success != 1 {
		t.Fatal("request settled more than once", success)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-request", "10.000", "0.000", "10.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE payee_wallet_id=$1::uuid`, f.payee.ID); n != 1 {
		t.Fatal(n)
	}
}
func TestPGWalletTransferRequestVisibilityPermissionAndExpiry(t *testing.T) {
	f := newRequestFixture(t)
	q := f.create(t)
	backendCall(t, f.app, "POST", "/wallet/wallets/"+f.payee.ID+"/public-id/disable", f.otherToken, "", 200)
	backendCall(t, f.app, "GET", "/wallet/wallets/transfer/requests/"+q.ID, "", "", 404)
	f.pay(t, q.ID, 404)
	backendCall(t, f.app, "POST", "/wallet/wallets/"+f.payee.ID+"/public-id/enable", f.otherToken, "", 200)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.other)
	backendCall(t, f.app, "GET", "/api/wallets/transfer/requests/"+q.ID, "", "", 404)
	f.pay(t, q.ID, 404)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, f.other)
	query(t, f.db, `UPDATE wallet_v1.wallets SET deleted_at=clock_timestamp() WHERE id=$1::uuid`, f.payee.ID)
	backendCall(t, f.app, "GET", "/api/wallets/transfer/requests/"+q.ID, "", "", 404)
	f.pay(t, q.ID, 404)
	query(t, f.db, `UPDATE wallet_v1.wallets SET deleted_at=NULL WHERE id=$1::uuid`, f.payee.ID)
	query(t, f.db, `UPDATE wallet_v1.transfer_requests SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1::uuid`, q.ID)
	backendCall(t, f.app, "GET", "/api/wallets/transfer/requests/"+q.ID, "", "", 404)
	f.pay(t, q.ID, 404)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.other, []authority.Rule{{Pattern: "*"}, {Pattern: w.PermissionTransferRequests, Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	backendJSONCall(t, f.app, "POST", "/wallet/wallets/transfer/requests", f.otherToken, map[string]any{"amount": 10, "currency": "synthetic-request", "wallet_id": f.payee.ID}, 403)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE payer_wallet_id=$1::uuid`, f.payer.ID); n != 0 {
		t.Fatal("denied request spent money")
	}
}
func TestPGWalletTransferRequestRollbackAndBounds(t *testing.T) {
	f := newRequestFixture(t)
	for _, hours := range []int{0, 169} {
		backendJSONCall(t, f.app, "POST", "/wallet/wallets/transfer/requests", f.otherToken, map[string]any{"amount": 10, "currency": "synthetic-request", "wallet_id": f.payee.ID, "expiration_hours": hours}, 400)
	}
	backendJSONCall(t, f.app, "POST", "/wallet/wallets/transfer/requests", f.token, map[string]any{"amount": 10, "currency": "synthetic-request", "wallet_id": f.payee.ID}, 403)
	q := f.create(t)
	name := "synthetic_request_rollback_" + strings.ReplaceAll(q.ID, "-", "")
	query(t, f.db, `ALTER TABLE wallet_v1.transfer_requests ADD CONSTRAINT `+name+` CHECK(id<>'`+q.ID+`'::uuid OR status<>1) NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE wallet_v1.transfer_requests DROP CONSTRAINT IF EXISTS ` + name) })
	f.pay(t, q.ID, 500)
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.actor); n != 0 {
		t.Fatal("rolled back request consumed payment proof", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NULL`, f.actor); n != 1 {
		t.Fatal("failed transaction lost retryable payment proof", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "synthetic-request", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.transactions WHERE payer_wallet_id=$1::uuid`, f.payer.ID); n != 0 {
		t.Fatal("failed request committed transfer")
	}
	backendCall(t, f.app, "GET", "/wallet/wallets/transfer/requests/"+q.ID, "", "", 200)
	query(t, f.db, `ALTER TABLE wallet_v1.transfer_requests DROP CONSTRAINT `+name)
	f.pay(t, q.ID, 200)
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.actor); n != 1 {
		t.Fatal("successful retry proof count", n)
	}
}
func TestPGWalletTransferRequestExpiresDuringWalletWait(t *testing.T) {
	f := newRequestFixture(t)
	q := f.create(t)
	clock := &atomic.Int64{}
	clock.Store(time.Now().UTC().UnixMicro())
	now := func() time.Time { return time.UnixMicro(clock.Load()).UTC() }
	query(t, f.db, `UPDATE wallet_v1.transfer_requests SET expires_at=$2::timestamptz WHERE id=$1::uuid`, q.ID, now().Add(time.Minute))
	canonical := authoritypg.New(f.db)
	verifier, e := authority.NewVerifier(canonical, f.config)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(canonical, verifier)
	pin := walletpg.NewPINVerifier(f.db, nil).WithAuthority(guard).WithClock(now)
	repo := walletpg.NewWithAuthority(f.db, guard).WithClock(now).WithPaymentAuthorization(pin)
	handler := w.NewAuthorityHandler(repo, verifier, func(p authority.Principal) w.Principal {
		return w.Principal{AccountID: p.Account.ID, SessionID: p.Session.ID}
	}, walletpg.NewRealmAuthority(f.db), pin.Atomic(), func(e error) error {
		var failure *authority.Failure
		if errors.As(e, &failure) {
			if failure.Status == 401 {
				return w.ErrUnauthorized
			}
			if failure.Status == 403 || failure.Status == 423 {
				return w.ErrForbidden
			}
		}
		return w.ErrUnavailable
	})
	composed := http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		p, e := verifier.AuthenticateRequest(r)
		if e != nil {
			t.Error(e)
			out.WriteHeader(401)
			return
		}
		handler.ServeHTTP(out, r.WithContext(guard.Bind(r.Context(), p)))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	af := atomicAuthorityFixture{db: f.db, ctx: ctx}
	tx, pid := atomicBlocker(t, af, "wallet_v1.wallets", f.payee.ID)
	done := make(chan int, 1)
	body, _ := json.Marshal(map[string]any{"transfer_request_id": q.ID, "payer_wallet_id": f.payer.ID})
	go func() {
		v := httptest.NewRecorder()
		composed.ServeHTTP(v, fixtureRawJSONRequest("POST", "/api/wallets/transfer", f.token, string(body)).WithContext(ctx))
		done <- v.Code
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case status := <-done:
			t.Fatalf("transfer completed before expected wallet lock: HTTP %d", status)
		default:
		}
		var blocked int
		e := f.db.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid)) AND position('wallet_v1.wallets' in query)>0 LIMIT 1`, pid).Scan(&blocked)
		if e == nil {
			break
		}
		if !errors.Is(e, sql.ErrNoRows) {
			t.Fatal(e)
		}
		select {
		case status := <-done:
			t.Fatalf("transfer completed before expected wallet lock: HTTP %d", status)
		case <-ctx.Done():
			t.Fatal("expected SQL lock wait was not observed", ctx.Err())
		case <-ticker.C:
		}
	}
	clock.Add(int64(61 * time.Second / time.Microsecond))
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if status := <-done; status != 409 {
		t.Fatal("expired locked request accepted", status)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "synthetic-request", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.actor); n != 0 {
		t.Fatal("expiry consumed payment proof", n)
	}
}
func TestPGWalletTransferRequestRestartPersistence(t *testing.T) {
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "wallet-transfer-request.json")
	type state struct{ Active, Paid, Transaction string }
	if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var saved state
		if e = json.Unmarshal(raw, &saved); e != nil {
			t.Fatal(e)
		}
		repo := walletpg.New(db)
		got, e := repo.PublicTransferRequest(context.Background(), saved.Active)
		if e != nil || got.ID != saved.Active {
			t.Fatal(got, e)
		}
		var linked string
		if e = db.QueryRow(`SELECT transaction_id::text FROM wallet_v1.transfer_requests WHERE id=$1::uuid AND status=1`, saved.Paid).Scan(&linked); e != nil || linked != saved.Transaction {
			t.Fatal(linked, e)
		}
		return
	}
	f := newRequestFixture(t)
	active := f.create(t)
	paid := f.create(t)
	tr := backendDecode[w.Transaction](t, f.pay(t, paid.ID, 200))
	raw, _ := json.Marshal(state{active.ID, paid.ID, tr.ID})
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
