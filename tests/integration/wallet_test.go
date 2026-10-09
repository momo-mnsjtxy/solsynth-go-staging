package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Synthetic-only disposable PostgreSQL fixtures. The parent harness must load
// walletpg.SchemaSQL before these tests; no production funding/account occurs.
func walletDB(t *testing.T) *sql.DB {
	t.Helper()
	db := database(t)
	var exists bool
	if e := db.QueryRow(`SELECT to_regclass('wallet_v1.wallets') IS NOT NULL`).Scan(&exists); e != nil || !exists {
		t.Fatal("wallet_v1 schema missing; load internal/walletpg/schema.sql in disposable harness", e)
	}
	return db
}
func walletID(n int) string { return fmt.Sprintf("a7000000-0000-0000-0000-%012d", n) }
func walletAmount(t *testing.T, s string) accounts.Amount {
	t.Helper()
	v, e := accounts.ParseAmount(s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func walletCreate(t *testing.T, r *walletpg.Repository, account string) accounts.Wallet {
	t.Helper()
	w, e := r.CreateWallet(context.Background(), accounts.Owner{AccountID: account}, "Synthetic Wallet")
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func walletFund(t *testing.T, db *sql.DB, w accounts.Wallet, currency, amount string) {
	t.Helper()
	query(t, db, `INSERT INTO wallet_v1.pockets(id,wallet_id,currency,amount,created_at,updated_at) VALUES($1,$2,$3,$4,now(),now())`, walletID(int(time.Now().UnixNano()%900000000000)), w.ID, currency, amount)
}
func walletBalance(t *testing.T, r *walletpg.Repository, id, currency string) (string, string, string) {
	t.Helper()
	w, e := r.GetWallet(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range w.Pockets {
		if p.Currency == currency {
			return p.Amount.String(), p.HeldAmount.String(), p.AvailableAmount.String()
		}
	}
	return "0.000", "0.000", "0.000"
}
func walletAssertBalance(t *testing.T, r *walletpg.Repository, id, currency, amount, held, available string) {
	t.Helper()
	a, h, v := walletBalance(t, r, id, currency)
	if a != amount || h != held || v != available {
		t.Fatalf("%s balance=%s held=%s available=%s want=%s/%s/%s", currency, a, h, v, amount, held, available)
	}
}

type walletAccess struct {
	realm  string
	editor bool
	deny   bool
}

func (a walletAccess) Permission(context.Context, accounts.Principal, string) error {
	if a.deny {
		return accounts.ErrForbidden
	}
	return nil
}
func (a walletAccess) Realm(_ context.Context, _ accounts.Principal, id string, role accounts.RealmRole) error {
	if a.realm != id || (role == accounts.Editor && !a.editor) {
		return accounts.ErrForbidden
	}
	return nil
}
func (a walletAccess) RealmIDs(context.Context, accounts.Principal) ([]string, error) {
	if a.realm == "" {
		return []string{}, nil
	}
	return []string{a.realm}, nil
}

type walletPIN struct{}

func (walletPIN) VerifyPayment(_ context.Context, _ accounts.Principal, pin string) error {
	if pin != "synthetic-proof" {
		return accounts.ErrPIN
	}
	return nil
}

type walletAuth struct{ account string }

func (a walletAuth) Authenticate(*http.Request) (accounts.Principal, error) {
	return accounts.Principal{AccountID: a.account, SessionID: "disposable-test-session"}, nil
}
func walletService(r *walletpg.Repository, access walletAccess) *accounts.Service {
	return &accounts.Service{Store: r, Access: access, Payments: walletPIN{}}
}
func walletRequest(s *accounts.Service, account, method, path, body, key string) *httptest.ResponseRecorder {
	out := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	accounts.NewHandler(s, walletAuth{account}).ServeHTTP(out, req)
	return out
}
func walletReadJSON[T any](t *testing.T, out *httptest.ResponseRecorder, status int) T {
	t.Helper()
	var v T
	if out.Code != status {
		t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
	}
	if e := json.Unmarshal(out.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func walletTransfer(t *testing.T, s *accounts.Service, actor string, payer, payee accounts.Wallet, amount, currency, key string, freeze, confirm bool) (accounts.Transaction, error) {
	t.Helper()
	pin := "synthetic-proof"
	return s.Transfer(context.Background(), accounts.Principal{AccountID: actor, SessionID: "test-session"}, accounts.TransferRequest{Amount: walletAmount(t, amount), Currency: currency, PayerWalletID: &payer.ID, PayeeWalletID: &payee.ID, PINCode: &pin, Freeze: freeze, RequireConfirmation: confirm}, key)
}

func TestPGWalletHTTPFamily(t *testing.T) {
	db := walletDB(t)
	r := walletpg.New(db)
	s := walletService(r, walletAccess{})
	a, b := walletID(1001), walletID(1002)
	if out := walletRequest(s, a, "GET", "/api/wallets", "", ""); out.Code != 404 {
		t.Fatal(out.Code, out.Body.String())
	}
	first := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "POST", "/api/wallets", `{"name":" First "}`, ""), 200)
	if !first.IsPrimary || first.Name != "First" || first.Pockets == nil {
		t.Fatal(first)
	}
	second := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "POST", "/api/wallets", `{"name":"Second"}`, ""), 200)
	walletCreate(t, r, a)
	if out := walletRequest(s, a, "POST", "/api/wallets", `{}`, ""); out.Code != 400 {
		t.Fatal(out.Code, out.Body.String())
	}
	changed := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "POST", "/api/wallets/"+second.ID+"/default", "", ""), 200)
	if !changed.IsPrimary {
		t.Fatal(changed)
	}
	got := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "GET", "/api/wallets", "", ""), 200)
	if got.ID != second.ID {
		t.Fatal(got)
	}
	list := walletReadJSON[[]accounts.Wallet](t, walletRequest(s, a, "GET", "/api/wallets/all", "", ""), 200)
	if len(list) != 3 || list[0].ID != second.ID {
		t.Fatal(list)
	}
	if out := walletRequest(s, b, "GET", "/api/wallets/"+first.ID, "", ""); out.Code != 403 {
		t.Fatal("ownership", out.Code)
	}
	enabled := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "POST", "/api/wallets/"+first.ID+"/public-id/enable", "", ""), 200)
	if enabled.PublicID == nil || !strings.HasPrefix(*enabled.PublicID, "DNW-") {
		t.Fatal(enabled)
	}
	repeated := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "POST", "/api/wallets/"+first.ID+"/public-id/enable", "", ""), 200)
	if repeated.PublicID == nil || *repeated.PublicID != *enabled.PublicID {
		t.Fatal("public ID repeat changed")
	}
	disabled := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "POST", "/api/wallets/"+first.ID+"/public-id/disable", "", ""), 200)
	if disabled.PublicID != nil {
		t.Fatal(disabled)
	}
	if _, e := r.WalletByPublicID(context.Background(), *enabled.PublicID); e != accounts.ErrNotFound {
		t.Fatal(e)
	}
	realm := walletID(1003)
	body := `{"realm_id":"` + realm + `"}`
	if out := walletRequest(s, a, "POST", "/api/wallets", body, ""); out.Code != 403 {
		t.Fatal(out.Code)
	}
	s.Access = walletAccess{realm: realm, editor: true}
	rw := walletReadJSON[accounts.Wallet](t, walletRequest(s, a, "POST", "/api/wallets", body, ""), 200)
	if rw.RealmID == nil || rw.AccountID != nil {
		t.Fatal(rw)
	}
	s.Access = walletAccess{realm: realm}
	list = walletReadJSON[[]accounts.Wallet](t, walletRequest(s, a, "GET", "/api/wallets/all", "", ""), 200)
	if len(list) != 4 {
		t.Fatal(list)
	}
	if out := walletRequest(s, a, "POST", "/api/wallets/"+rw.ID+"/public-id/enable", "", ""); out.Code != 403 {
		t.Fatal("viewer altered realm", out.Code)
	}
	query(t, db, `UPDATE wallet_v1.wallets SET deleted_at=now() WHERE id=$1`, first.ID)
	if out := walletRequest(s, a, "GET", "/api/wallets/"+first.ID, "", ""); out.Code != 404 {
		t.Fatal("tombstone exposed", out.Code)
	}
}

func TestPGWalletAtomicPrecisionRollbackAndIdempotency(t *testing.T) {
	db := walletDB(t)
	r := walletpg.New(db)
	s := walletService(r, walletAccess{})
	a, b := walletID(2001), walletID(2002)
	payer, payee := walletCreate(t, r, a), walletCreate(t, r, b)
	walletFund(t, db, payer, "points", "9007199254741000.000")
	tr, e := walletTransfer(t, s, a, payer, payee, "9007199254740993.1239", "points", "exact", false, false)
	if e != nil {
		t.Fatal(e)
	}
	if tr.Amount.String() != "9007199254740993.123" || tr.Status != accounts.Confirmed {
		t.Fatal(tr)
	}
	walletAssertBalance(t, r, payer.ID, "points", "6.877", "0.000", "6.877")
	walletAssertBalance(t, r, payee.ID, "points", "9007199254740993.123", "0.000", "9007199254740993.123")
	again, e := walletTransfer(t, s, a, payer, payee, "9007199254740993.1239", "points", "exact", false, false)
	if e != nil || again.ID != tr.ID {
		t.Fatal(again, e)
	}
	if _, e = walletTransfer(t, s, a, payer, payee, "1", "points", "exact", false, false); !errors.Is(e, accounts.ErrIdempotency) {
		t.Fatal(e)
	}
	if _, e = walletTransfer(t, s, a, payer, payee, "7", "points", "insufficient", false, false); !errors.Is(e, accounts.ErrInsufficient) {
		t.Fatal(e)
	}
	walletAssertBalance(t, r, payer.ID, "points", "6.877", "0.000", "6.877")
	walletFund(t, db, payer, "overflow", "1")
	walletFund(t, db, payee, "overflow", "79228162514264337593543950335")
	if _, e = walletTransfer(t, s, a, payer, payee, "1", "overflow", "overflow-rollback", false, false); e == nil {
		t.Fatal("credit overflow accepted")
	}
	walletAssertBalance(t, r, payer.ID, "overflow", "1.000", "0.000", "1.000")
	walletAssertBalance(t, r, payee.ID, "overflow", "79228162514264337593543950335.000", "0.000", "79228162514264337593543950335.000")
	var count int
	query(t, db, `CREATE FUNCTION wallet_v1.reject_synthetic_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.payload->>'currency'='rollback-only' THEN RAISE EXCEPTION 'synthetic rollback'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_synthetic_outbox BEFORE INSERT ON wallet_v1.outbox FOR EACH ROW EXECUTE FUNCTION wallet_v1.reject_synthetic_outbox()`)
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS reject_synthetic_outbox ON wallet_v1.outbox;DROP FUNCTION IF EXISTS wallet_v1.reject_synthetic_outbox()`)
	})
	walletFund(t, db, payer, "rollback-only", "5")
	if _, e = walletTransfer(t, s, a, payer, payee, "2", "rollback-only", "rollback", false, false); e == nil {
		t.Fatal("injected outbox failure accepted")
	}
	walletAssertBalance(t, r, payer.ID, "rollback-only", "5.000", "0.000", "5.000")
	walletAssertBalance(t, r, payee.ID, "rollback-only", "0.000", "0.000", "0.000")
	if e = db.QueryRow(`SELECT count(*) FROM wallet_v1.transactions WHERE actor_id=$1 AND idempotency_key='rollback'`, a).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM wallet_v1.outbox WHERE aggregate_id=$1 AND event_type='wallet.transaction.created' AND published_at IS NULL`, tr.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("outbox/idempotency", count, e)
	}
	out := walletRequest(s, b, "GET", "/api/wallets/transactions?wallet="+payee.ID+"&direction=income", "", "")
	list := walletReadJSON[[]accounts.Transaction](t, out, 200)
	if len(list) != 1 || out.Header().Get("X-Total") != "1" || list[0].ID != tr.ID {
		t.Fatal(out.Body.String())
	}
	if out = walletRequest(s, walletID(2003), "GET", "/api/wallets/transactions/"+tr.ID, "", ""); out.Code != 404 {
		t.Fatal("transaction disclosure", out.Code)
	}
}

func TestPGWalletConcurrentDebitAndDuplicate(t *testing.T) {
	db := walletDB(t)
	r := walletpg.New(db)
	s := walletService(r, walletAccess{})
	a, b := walletID(3001), walletID(3002)
	payer, payee := walletCreate(t, r, a), walletCreate(t, r, b)
	walletFund(t, db, payer, "points", "10")
	var successes atomic.Int64
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := walletTransfer(t, s, a, payer, payee, "1", "points", fmt.Sprintf("parallel-%d", i), false, false)
			if e == nil {
				successes.Add(1)
			} else if !errors.Is(e, accounts.ErrInsufficient) {
				errs <- e
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if successes.Load() != 10 {
		t.Fatal(successes.Load())
	}
	walletAssertBalance(t, r, payer.ID, "points", "0.000", "0.000", "0.000")
	walletAssertBalance(t, r, payee.ID, "points", "10.000", "0.000", "10.000")
	walletFund(t, db, payer, "duplicates", "20")
	ids := make(chan string, 12)
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr, e := walletTransfer(t, s, a, payer, payee, "3", "duplicates", "same-key", false, false)
			if e != nil {
				errs <- e
			} else {
				ids <- tr.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	one := ""
	for id := range ids {
		if one == "" {
			one = id
		} else if one != id {
			t.Fatal("duplicate created another transaction")
		}
	}
	walletAssertBalance(t, r, payer.ID, "duplicates", "17.000", "0.000", "17.000")
	walletAssertBalance(t, r, payee.ID, "duplicates", "3.000", "0.000", "3.000")
	walletFund(t, db, payer, "opposite", "100")
	walletFund(t, db, payee, "opposite", "100")
	errs = make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			from, to, actor := payer, payee, a
			if i%2 == 1 {
				from, to, actor = payee, payer, b
			}
			_, e := walletTransfer(t, s, actor, from, to, "1", "opposite", fmt.Sprintf("opposed-%d", i), false, false)
			if e != nil {
				errs <- e
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	walletAssertBalance(t, r, payer.ID, "opposite", "100.000", "0.000", "100.000")
	walletAssertBalance(t, r, payee.ID, "opposite", "100.000", "0.000", "100.000")
}

func TestPGWalletHeldLifecycleAndExpiry(t *testing.T) {
	db := walletDB(t)
	var clock atomic.Int64
	start := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	clock.Store(start.Unix())
	r := walletpg.NewWithClock(db, func() time.Time { return time.Unix(clock.Load(), 0).UTC() })
	s := walletService(r, walletAccess{})
	a, b := walletID(4001), walletID(4002)
	payer, payee := walletCreate(t, r, a), walletCreate(t, r, b)
	walletFund(t, db, payer, "points", "100")
	tr, e := walletTransfer(t, s, a, payer, payee, "20", "points", "freeze-and-confirm", true, true)
	if e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, r, payer.ID, "points", "80.000", "20.000", "80.000")
	walletAssertBalance(t, r, payee.ID, "points", "0.000", "0.000", "0.000")
	if out := walletRequest(s, a, "POST", "/api/wallets/transactions/"+tr.ID+"/confirm", "", ""); out.Code != 403 {
		t.Fatal("payer confirmed", out.Code)
	}
	accepted := walletReadJSON[accounts.Transaction](t, walletRequest(s, b, "POST", "/api/wallets/transactions/"+tr.ID+"/confirm", "", ""), 200)
	if accepted.Status != accounts.Frozen || accepted.ConfirmationReceivedAt == nil {
		t.Fatal(accepted)
	}
	walletAssertBalance(t, r, payer.ID, "points", "80.000", "20.000", "80.000")
	if n, e := r.ProcessDue(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	clock.Store(start.Add(24 * time.Hour).Unix())
	if n, e := r.ProcessDue(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := r.ProcessDue(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, r, payer.ID, "points", "80.000", "0.000", "80.000")
	walletAssertBalance(t, r, payee.ID, "points", "20.000", "0.000", "20.000")
	pending, e := walletTransfer(t, s, a, payer, payee, "5", "points", "expire-manual", false, true)
	if e != nil {
		t.Fatal(e)
	}
	out := walletRequest(s, b, "GET", "/api/wallets/transactions/pending", "", "")
	list := walletReadJSON[[]accounts.Transaction](t, out, 200)
	if len(list) != 1 || list[0].ID != pending.ID || out.Header().Get("X-Total") != "1" {
		t.Fatal(list, out.Header())
	}
	clock.Store(start.Add(48 * time.Hour).Unix())
	if n, e := r.ProcessDue(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, r, payer.ID, "points", "80.000", "0.000", "80.000")
	got, e := r.GetTransaction(context.Background(), pending.ID)
	if e != nil || got.Status != accounts.Refunded {
		t.Fatal(got, e)
	}
}

func TestPGWalletConcurrentQuotaDefaultAndConfirm(t *testing.T) {
	db := walletDB(t)
	r := walletpg.New(db)
	a, b := walletID(5001), walletID(5002)
	var wg sync.WaitGroup
	var successes atomic.Int64
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.CreateWallet(context.Background(), accounts.Owner{AccountID: a}, "Concurrent")
			if e == nil {
				successes.Add(1)
			} else if e != accounts.ErrQuota {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if successes.Load() != 3 {
		t.Fatal(successes.Load())
	}
	wallets, e := r.ListWallets(context.Background(), a, nil)
	if e != nil || len(wallets) != 3 {
		t.Fatal(e, wallets)
	}
	errs = make(chan error, 3)
	for _, w := range wallets {
		wg.Add(1)
		go func(w accounts.Wallet) {
			defer wg.Done()
			_, e := r.SetDefault(context.Background(), w.ID, a)
			if e != nil {
				errs <- e
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	var primary int
	if e = db.QueryRow(`SELECT count(*) FROM wallet_v1.wallets WHERE account_id=$1 AND is_primary AND deleted_at IS NULL`, a).Scan(&primary); e != nil || primary != 1 {
		t.Fatal(primary, e)
	}
	payer, payee := wallets[0], walletCreate(t, r, b)
	walletFund(t, db, payer, "points", "10")
	s := walletService(r, walletAccess{})
	tr, e := walletTransfer(t, s, a, payer, payee, "4", "points", "race-confirm", false, true)
	if e != nil {
		t.Fatal(e)
	}
	errs = make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.Transition(context.Background(), tr.ID, accounts.Owner{AccountID: b}, accounts.Confirm)
			if e != nil {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	walletAssertBalance(t, r, payer.ID, "points", "6.000", "0.000", "6.000")
	walletAssertBalance(t, r, payee.ID, "points", "4.000", "0.000", "4.000")
}

func TestPGWalletRestartPersistence(t *testing.T) {
	db := walletDB(t)
	r := walletpg.New(db)
	ctx := context.Background()
	account, recipient := walletID(6001), walletID(6002)
	if os.Getenv("PG_TEST_PHASE") == "before" {
		payer, payee := walletCreate(t, r, account), walletCreate(t, r, recipient)
		walletFund(t, db, payer, "restart-points", "123.456")
		if _, e := r.SetPublicID(ctx, payer.ID, payer.Owner(), true); e != nil {
			t.Fatal(e)
		}
		if _, e := walletTransfer(t, walletService(r, walletAccess{}), account, payer, payee, "23.456", "restart-points", "restart-transfer", false, false); e != nil {
			t.Fatal(e)
		}
	} else if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("missing restart phase")
	}
	// After restart this branch never recreates data or funding.
	independent := walletpg.New(walletDB(t))
	payer, e := independent.DefaultWallet(ctx, account)
	if e != nil || payer.PublicID == nil {
		t.Fatal(payer, e)
	}
	payee, e := independent.DefaultWallet(ctx, recipient)
	if e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, independent, payer.ID, "restart-points", "100.000", "0.000", "100.000")
	walletAssertBalance(t, independent, payee.ID, "restart-points", "23.456", "0.000", "23.456")
	page, e := independent.Transactions(ctx, accounts.TransactionQuery{WalletID: payer.ID, Take: 20})
	if e != nil || len(page.Items) != 1 || page.Items[0].Amount.String() != "23.456" || page.Items[0].Status != accounts.Confirmed {
		t.Fatal(page, e)
	}
	var pending int
	if e = db.QueryRow(`SELECT count(*) FROM wallet_v1.outbox WHERE aggregate_id=$1 AND published_at IS NULL`, page.Items[0].ID).Scan(&pending); e != nil || pending != 1 {
		t.Fatal(pending, e)
	}
}

func TestPGWalletDurablePINPolicyAndLockout(t *testing.T) {
	db := walletDB(t)
	r := walletpg.New(db)
	a, b := walletID(7001), walletID(7002)
	payer, payee := walletCreate(t, r, a), walletCreate(t, r, b)
	walletFund(t, db, payer, "points", "9")
	// All credential material below is ephemeral synthetic test data. No real
	// account or external authentication system is used.
	pepper := make([]byte, 32)
	if _, e := rand.Read(pepper); e != nil {
		t.Fatal(e)
	}
	verifier := walletpg.NewPINVerifier(db, map[string][]byte{"synthetic-ephemeral": pepper})
	s := walletService(r, walletAccess{})
	s.Payments = verifier
	p := accounts.Principal{AccountID: a, SessionID: "synthetic-session"}
	request := accounts.TransferRequest{Amount: walletAmount(t, "1"), Currency: "points", PayerWalletID: &payer.ID, PayeeWalletID: &payee.ID}
	if _, e := s.Transfer(context.Background(), p, request, "no-policy"); e != accounts.ErrUnavailable {
		t.Fatal(e)
	}
	e := verifier.ProvisionPIN(context.Background(), a, "482971", "synthetic-ephemeral")
	if e != nil {
		t.Fatal(e)
	}
	status := walletReadJSON[accounts.PINStatus](t, walletRequest(s, a, "GET", "/api/accounts/me/pin-status", "", ""), 200)
	if !status.HasPIN || !status.ValidationRequired {
		t.Fatal(status)
	}
	bad := "NO_PIN_PROVEDED"
	request.PINCode = &bad
	for i := 0; i < 5; i++ {
		if _, e = s.Transfer(context.Background(), p, request, "wrong"); e != accounts.ErrPIN {
			t.Fatal(e)
		}
	}
	walletAssertBalance(t, r, payer.ID, "points", "9.000", "0.000", "9.000")
	// Recreating verifier does not reset the database attempt limit.
	s.Payments = walletpg.NewPINVerifier(db, map[string][]byte{"synthetic-ephemeral": pepper})
	good := "482971"
	request.PINCode = &good
	if _, e = s.Transfer(context.Background(), p, request, "locked-correct"); e != accounts.ErrPIN {
		t.Fatal(e)
	}
	query(t, db, `UPDATE wallet_v1.payment_policies SET locked_until=now()-interval '1 minute' WHERE account_id=$1`, a)
	if _, e = s.Transfer(context.Background(), p, request, "verified"); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, r, payer.ID, "points", "8.000", "0.000", "8.000")
	var attempts int
	if e = db.QueryRow(`SELECT failed_attempts FROM wallet_v1.payment_policies WHERE account_id=$1`, a).Scan(&attempts); e != nil || attempts != 0 {
		t.Fatal(attempts, e)
	}
	query(t, db, `UPDATE wallet_v1.payment_policies SET disabled=true WHERE account_id=$1`, a)
	if _, e = s.Transfer(context.Background(), p, request, "disabled"); e != accounts.ErrUnavailable {
		t.Fatal(e)
	}
	// Explicit trusted no-PIN policy is distinct from a missing policy.
	query(t, db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at) VALUES($1,false,now())`, b)
	if e = verifier.VerifyPayment(context.Background(), accounts.Principal{AccountID: b, SessionID: "synthetic"}, ""); e != nil {
		t.Fatal(e)
	}
}

func TestPGWalletFreshRealmGrantPolicy(t *testing.T) {
	db := walletDB(t)
	ctx := context.Background()
	a, realm := walletID(8001), walletID(8002)
	principal := accounts.Principal{AccountID: a, SessionID: "synthetic"}
	roles := walletpg.NewRealmAuthority(db)
	if e := roles.Realm(ctx, principal, realm, accounts.Viewer); e != accounts.ErrForbidden {
		t.Fatal(e)
	}
	query(t, db, `INSERT INTO wallet_v1.realm_grants(account_id,realm_id,role,updated_at) VALUES($1,$2,'viewer',now())`, a, realm)
	if e := roles.Realm(ctx, principal, realm, accounts.Viewer); e != nil {
		t.Fatal(e)
	}
	if e := roles.Realm(ctx, principal, realm, accounts.Editor); e != accounts.ErrForbidden {
		t.Fatal(e)
	}
	ids, e := roles.RealmIDs(ctx, principal)
	if e != nil || len(ids) != 1 || ids[0] != realm {
		t.Fatal(ids, e)
	}
	query(t, db, `UPDATE wallet_v1.realm_grants SET role='editor' WHERE account_id=$1 AND realm_id=$2`, a, realm)
	if e = roles.Realm(ctx, principal, realm, accounts.Editor); e != nil {
		t.Fatal(e)
	}
	query(t, db, `UPDATE wallet_v1.realm_grants SET expires_at=now()-interval '1 second' WHERE account_id=$1 AND realm_id=$2`, a, realm)
	if e = roles.Realm(ctx, principal, realm, accounts.Viewer); e != accounts.ErrForbidden {
		t.Fatal(e)
	}
	ids, e = roles.RealmIDs(ctx, principal)
	if e != nil || len(ids) != 0 {
		t.Fatal(ids, e)
	}
	query(t, db, `UPDATE wallet_v1.realm_grants SET expires_at=NULL,revoked_at=now() WHERE account_id=$1 AND realm_id=$2`, a, realm)
	if e = roles.Realm(ctx, principal, realm, accounts.Editor); e != accounts.ErrForbidden {
		t.Fatal(e)
	}
}

func TestPGWalletVerifiedAuthorityComposition(t *testing.T) {
	db := walletDB(t)
	ctx := context.Background()
	query(t, db, authoritypg.Schema)
	now := time.Now().UTC().Truncate(time.Second)
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	hash, seal := make([]byte, 32), make([]byte, 32)
	if _, e = rand.Read(hash); e != nil {
		t.Fatal(e)
	}
	if _, e = rand.Read(seal); e != nil {
		t.Fatal(e)
	}
	config := authority.Config{Issuer: "https://synthetic-wallet.invalid", Audience: "synthetic-wallet", SigningKeyID: "ephemeral", SigningKey: key, TrustedKeys: map[string]*rsa.PublicKey{"ephemeral": &key.PublicKey}, RefreshHashKey: hash, ReplaySealKey: seal, Now: func() time.Time { return now }}
	authStore := authoritypg.New(db)
	issuer, e := authority.New(authStore, config)
	if e != nil {
		t.Fatal(e)
	}
	verifier, e := authority.NewVerifier(authStore, authority.Config{Issuer: config.Issuer, Audience: config.Audience, TrustedKeys: config.TrustedKeys, Now: config.Now})
	if e != nil {
		t.Fatal(e)
	}
	a, b := walletID(9001), walletID(9002)
	for _, account := range []string{a, b} {
		if e = authoritypg.SeedSynthetic(ctx, db, account, "synthetic_wallet_"+strings.ReplaceAll(account, "-", ""), now); e != nil {
			t.Fatal(e)
		}
	}
	pair, e := issuer.IssueSession(ctx, a, authority.SessionOptions{Label: "synthetic wallet integration", Scopes: []string{"wallets.*"}})
	if e != nil {
		t.Fatal(e)
	}
	r := walletpg.New(db)
	payer, payee := walletCreate(t, r, a), walletCreate(t, r, b)
	walletFund(t, db, payer, "points", "10")
	query(t, db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at) VALUES($1,false,now())`, a)
	mapError := func(e error) error {
		var failure *authority.Failure
		if errors.As(e, &failure) {
			switch failure.Status {
			case 401:
				return accounts.ErrUnauthorized
			case 403, 423:
				return accounts.ErrForbidden
			}
		}
		return accounts.ErrUnavailable
	}
	h := accounts.NewAuthorityHandler(r, verifier, func(p authority.Principal) accounts.Principal {
		return accounts.Principal{AccountID: p.Account.ID, SessionID: p.Session.ID}
	}, walletpg.NewRealmAuthority(db), walletpg.NewPINVerifier(db, nil), mapError)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Account-ID", b)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		return out
	}
	walletReadJSON[accounts.Wallet](t, request("GET", "/api/wallets", "", pair.Token), 200)
	listed := walletReadJSON[[]accounts.Wallet](t, request("GET", "/api/wallets/all", "", pair.Token), 200)
	if len(listed) != 1 {
		t.Fatal(listed)
	}
	realm := walletID(9003)
	query(t, db, `INSERT INTO wallet_v1.realm_grants(account_id,realm_id,role,updated_at) VALUES($1,$2,'editor',now())`, a, realm)
	realmWallet := walletReadJSON[accounts.Wallet](t, request("POST", "/api/wallets", `{"realm_id":"`+realm+`"}`, pair.Token), 200)
	if realmWallet.RealmID == nil || *realmWallet.RealmID != realm {
		t.Fatal(realmWallet)
	}
	body := `{"amount":2,"currency":"points","payer_wallet_id":"` + payer.ID + `","payee_wallet_id":"` + payee.ID + `"}`
	if out := request("POST", "/api/wallets/transfer", body, pair.Token); out.Code != 403 {
		t.Fatal("token scopes became permissions", out.Code, out.Body.String())
	}
	if e = authStore.ReplacePolicy(ctx, a, []authority.Rule{{Pattern: accounts.PermissionTransactions}}, nil); e != nil {
		t.Fatal(e)
	}
	walletReadJSON[accounts.Transaction](t, request("POST", "/api/wallets/transfer", body, pair.Token), 200)
	walletAssertBalance(t, r, payer.ID, "points", "8.000", "0.000", "8.000")
	if e = authStore.ReplacePolicy(ctx, a, nil, nil); e != nil {
		t.Fatal(e)
	}
	if out := request("POST", "/api/wallets/transfer", body, pair.Token); out.Code != 403 {
		t.Fatal("stale grant accepted", out.Code)
	}
	if e = issuer.InvalidateAccount(ctx, a); e != nil {
		t.Fatal(e)
	}
	if out := request("GET", "/api/wallets", "", pair.Token); out.Code != 401 {
		t.Fatal("stale signed token accepted", out.Code)
	}
	walletAssertBalance(t, r, payer.ID, "points", "8.000", "0.000", "8.000")
}
