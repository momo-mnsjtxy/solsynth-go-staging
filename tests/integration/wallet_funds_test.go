// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	w "solsynth.local/go-migration/ports/wallet/accounts"
)

const fundCurrency = "synthetic-funds"

type fundPerson struct {
	actor, token string
	wallet       w.Wallet
}
type fundsFixture struct {
	backendFixture
	creator, one, two, three fundPerson
}

func newFundsFixture(t *testing.T) fundsFixture {
	t.Helper()
	f := fundsFixture{backendFixture: newBackendFixture(t)}
	f.creator = f.person(t, f.actor, f.token)
	f.one = f.person(t, f.other, f.otherToken)
	for _, target := range []*fundPerson{&f.two, &f.three} {
		id := authorityUUID(t)
		if e := authoritypg.SeedSynthetic(context.Background(), f.db, id, "synthetic_fund_"+strings.ReplaceAll(id, "-", ""), f.config.Now()); e != nil {
			t.Fatal(e)
		}
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
		*target = f.person(t, id, pgIssue(t, f.issuer, id).Token)
	}
	return f
}
func (f fundsFixture) person(t *testing.T, id, token string) fundPerson {
	t.Helper()
	p := fundPerson{actor: id, token: token, wallet: walletCreate(t, walletpg.New(f.db), id)}
	walletFund(t, f.db, p.wallet, fundCurrency, "100")
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1,false,$2)`, id, f.config.Now())
	return p
}
func (f fundsFixture) create(t *testing.T, q map[string]any) w.Fund {
	t.Helper()
	if q["currency"] == nil {
		q["currency"] = fundCurrency
	}
	if q["payer_wallet_id"] == nil {
		q["payer_wallet_id"] = f.creator.wallet.ID
	}
	if q["amount_of_splits"] == nil {
		q["amount_of_splits"] = 3
	}
	return backendDecode[w.Fund](t, backendJSONCall(t, f.app, "POST", "/wallet/wallets/funds", f.creator.token, q, 200))
}
func (f fundsFixture) claim(t *testing.T, id string, p fundPerson, status int) *httptest.ResponseRecorder {
	return backendCall(t, f.app, "POST", "/wallet/wallets/funds/"+id+"/receive", p.token, "", status)
}
func (f fundsFixture) contribute(t *testing.T, id string, p fundPerson, amount any, status int) *httptest.ResponseRecorder {
	return backendJSONCall(t, f.app, "POST", "/wallet/wallets/funds/"+id+"/contribute", p.token, map[string]any{"amount": amount}, status)
}
func (f fundsFixture) balance(t *testing.T, p fundPerson, available, held string) {
	walletAssertBalance(t, walletpg.New(f.db), p.wallet.ID, fundCurrency, available, held, available)
}
func (f fundsFixture) get(t *testing.T, id string) w.Fund {
	return backendDecode[w.Fund](t, backendCall(t, f.app, "GET", "/api/wallets/funds/"+id, "", "", 200))
}
func TestPGWalletFundsRedPacketExactClaimsAndPublicHTTP(t *testing.T) {
	f := newFundsFixture(t)
	q := f.create(t, map[string]any{"total_amount": 0.005, "recipient_account_ids": []string{f.one.actor, f.two.actor, f.three.actor}})
	if q.IsOpen || q.RemainingAmount.String() != "0.005" || len(q.Recipients) != 3 {
		t.Fatal(q)
	}
	f.balance(t, f.creator, "99.995", "0.005")
	f.claim(t, q.ID, f.creator, 403)
	want := []string{"0.002", "0.002", "0.001"}
	var ids []string
	for i, p := range []fundPerson{f.one, f.two, f.three} {
		tr := backendDecode[w.Transaction](t, f.claim(t, q.ID, p, 200))
		if tr.Amount.String() != want[i] || tr.Status != w.Confirmed || tr.PayerWalletID != f.creator.wallet.ID || tr.PayeeWalletID != p.wallet.ID {
			t.Fatal(tr)
		}
		again := backendDecode[w.Transaction](t, f.claim(t, q.ID, p, 200))
		if again.ID != tr.ID {
			t.Fatal("duplicate claim", again)
		}
		ids = append(ids, tr.ID)
	}
	got := f.get(t, q.ID)
	if got.Status != w.FundCompleted || got.RemainingAmount.Sign() != 0 {
		t.Fatal(got)
	}
	f.balance(t, f.creator, "99.995", "0.000")
	f.balance(t, f.one, "100.002", "0.000")
	list := backendCall(t, f.app, "GET", "/wallet/wallets/funds?take=20&status=2", f.token, "", 200)
	if list.Header().Get("X-Total") != "1" {
		t.Fatal(list.Header())
	}
	if other := backendCall(t, f.app, "GET", "/wallet/wallets/funds", f.otherToken, "", 200); other.Header().Get("X-Total") != "0" {
		t.Fatal(other.Body.String())
	}
	backendCall(t, f.app, "POST", "/wallet/wallets/funds/"+q.ID+"/cancel", f.token, "", 409)
	backendCall(t, f.app, "GET", "/api/wallets/funds/"+q.ID+"/contributors", "", "", 400)
	backendCall(t, f.app, "GET", "/api/wallets/funds/recipients", f.token, "", 404)
	backendCall(t, f.app, "GET", "/api/wallets/funds/"+q.ID+"/order", f.token, "", 404)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.fund_allocations a JOIN wallet_v1.fund_reservations r ON r.id=a.reservation_id WHERE r.fund_id=$1`, q.ID); n != 3 {
		t.Fatal(n)
	}
	for _, id := range ids {
		if _, e := walletpg.New(f.db).Transition(context.Background(), id, f.one.wallet.Owner(), w.Reject); !errors.Is(e, w.ErrNotFound) {
			t.Fatal(e)
		}
	}
}
func TestPGWalletFundsRaisingInvitesPayoutAndImmutableBeneficiary(t *testing.T) {
	f := newFundsFixture(t)
	q := f.create(t, map[string]any{"is_raising": true, "target_amount": 5.003, "is_open": false, "recipient_account_ids": []string{f.one.actor, f.two.actor}, "amount_of_splits": 2})
	alternate := walletCreate(t, walletpg.New(f.db), f.actor)
	backendCall(t, f.app, "POST", "/wallet/wallets/"+alternate.ID+"/default", f.token, "", 200)
	if _, e := f.db.Exec(`UPDATE wallet_v1.funds SET beneficiary_wallet_id=$2 WHERE id=$1`, q.ID, alternate.ID); e == nil {
		t.Fatal("mutable beneficiary")
	}
	f.contribute(t, q.ID, f.three, 1, 403)
	first := backendDecode[w.Transaction](t, f.contribute(t, q.ID, f.one, 2.001, 200))
	if first.Status != w.Frozen || first.PayerWalletID != f.one.wallet.ID || first.PayeeWalletID != f.creator.wallet.ID {
		t.Fatal(first)
	}
	f.balance(t, f.one, "97.999", "2.001")
	f.balance(t, f.creator, "100.000", "0.000")
	f.contribute(t, q.ID, f.two, 3.003, 400)
	last := backendDecode[w.Transaction](t, f.contribute(t, q.ID, f.two, 3.002, 200))
	if last.Status != w.Confirmed {
		t.Fatal(last)
	}
	f.balance(t, f.one, "97.999", "0.000")
	f.balance(t, f.two, "96.998", "0.000")
	f.balance(t, f.creator, "105.003", "0.000")
	got := f.get(t, q.ID)
	if got.Status != w.FundCompleted || got.RaisedAmount.String() != "5.003" || got.RemainingAmount.Sign() != 0 || got.BeneficiaryWalletID != f.creator.wallet.ID {
		t.Fatal(got)
	}
	replay := backendDecode[w.Transaction](t, f.contribute(t, q.ID, f.one, 2.001, 200))
	if replay.ID != first.ID || replay.Status != w.Confirmed {
		t.Fatal(replay)
	}
	f.contribute(t, q.ID, f.one, 2.002, 409)
	contributors := backendDecode[[]w.FundRecipient](t, backendCall(t, f.app, "GET", "/wallet/wallets/funds/"+q.ID+"/contributors", "", "", 200))
	if len(contributors) != 2 {
		t.Fatal(contributors)
	}
	backendCall(t, f.app, "POST", "/wallet/wallets/funds/"+q.ID+"/cancel", f.token, "", 409)
	if _, e := walletpg.NewWithClock(f.db, func() time.Time { return q.ExpiredAt.Add(time.Second) }).ProcessFundsDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	f.balance(t, f.creator, "105.003", "0.000")
	f.balance(t, f.one, "97.999", "0.000")
	if n := commerceCount(t, f.db, `SELECT count(*)FROM wallet_v1.fund_allocations a JOIN wallet_v1.fund_reservations r ON r.id=a.reservation_id WHERE r.fund_id=$1 AND a.kind='refund'`, q.ID); n != 0 {
		t.Fatal("successful double refund", n)
	}
}
func TestPGWalletFundsCancellationRefundsRemainingHoldsOnce(t *testing.T) {
	f := newFundsFixture(t)
	packet := f.create(t, map[string]any{"total_amount": 1.001, "amount_of_splits": 2})
	first := backendDecode[w.Transaction](t, f.claim(t, packet.ID, f.one, 200))
	if first.Amount.String() != "0.501" {
		t.Fatal(first)
	}
	backendCall(t, f.app, "POST", "/api/wallets/funds/"+packet.ID+"/cancel", f.otherToken, "", 403)
	for range 2 {
		cancelled := backendDecode[w.Fund](t, backendCall(t, f.app, "POST", "/api/wallets/funds/"+packet.ID+"/cancel", f.token, "", 200))
		if cancelled.Status != w.FundRefunded || cancelled.RemainingAmount.Sign() != 0 {
			t.Fatal(cancelled)
		}
	}
	f.balance(t, f.creator, "99.499", "0.000")
	f.balance(t, f.one, "100.501", "0.000")
	var refund, kind string
	var status int
	if e := f.db.QueryRow(`SELECT t.amount::text,t.fund_kind,t.status FROM wallet_v1.transactions t WHERE t.managed_by=$1 AND t.payer_wallet_id=t.payee_wallet_id`, packet.ID).Scan(&refund, &kind, &status); e != nil || refund != "0.500" || kind != "refund" || status != int(w.Refunded) {
		t.Fatal(refund, kind, status, e)
	}
	raising := f.create(t, map[string]any{"is_raising": true, "target_amount": 10, "amount_of_splits": 1, "is_open": true})
	contribution := backendDecode[w.Transaction](t, f.contribute(t, raising.ID, f.two, 2.345, 200))
	f.contribute(t, raising.ID, f.three, 2, 400)
	backendCall(t, f.app, "POST", "/api/wallets/funds/"+raising.ID+"/cancel", f.token, "", 200)
	replay := backendDecode[w.Transaction](t, f.contribute(t, raising.ID, f.two, 2.345, 200))
	if replay.ID != contribution.ID || replay.Status != w.Refunded {
		t.Fatal(replay)
	}
	f.balance(t, f.two, "100.000", "0.000")
	f.contribute(t, raising.ID, f.three, 1, 409)
	// A normal self transfer is still impossible despite managed self refunds.
	backendJSONCall(t, f.app, "POST", "/wallet/wallets/transfer", f.token, map[string]any{"payer_wallet_id": f.creator.wallet.ID, "payee_wallet_id": f.creator.wallet.ID, "currency": fundCurrency, "amount": 1}, 400)
}
func TestPGWalletFundsConcurrentFinalContributionAndClaims(t *testing.T) {
	f := newFundsFixture(t)
	q := f.create(t, map[string]any{"is_raising": true, "target_amount": 3.003, "is_open": true})
	f.contribute(t, q.ID, f.one, 1.001, 200)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var group sync.WaitGroup
	for _, p := range []fundPerson{f.two, f.three} {
		group.Add(1)
		go func(p fundPerson) {
			defer group.Done()
			<-start
			r := httptest.NewRecorder()
			f.app.ServeHTTP(r, fixtureRawJSONRequest("POST", "/wallet/wallets/funds/"+q.ID+"/contribute", p.token, `{"amount":2.002}`))
			codes <- r.Code
		}(p)
	}
	close(start)
	group.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	f.balance(t, f.creator, "103.003", "0.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.fund_reservations WHERE fund_id=$1`, q.ID); n != 2 {
		t.Fatal(n)
	}
	packet := f.create(t, map[string]any{"total_amount": 0.001, "amount_of_splits": 1})
	start = make(chan struct{})
	codes = make(chan int, 2)
	for _, p := range []fundPerson{f.one, f.two} {
		group.Add(1)
		go func(p fundPerson) {
			defer group.Done()
			<-start
			r := httptest.NewRecorder()
			f.app.ServeHTTP(r, fixtureNoBodyRequest("POST", "/wallet/wallets/funds/"+packet.ID+"/receive", p.token))
			codes <- r.Code
		}(p)
	}
	close(start)
	group.Wait()
	close(codes)
	success = 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.fund_participants WHERE fund_id=$1 AND received_at IS NOT NULL`, packet.ID); n != 1 {
		t.Fatal(n)
	}
}
func TestPGWalletFundsExpiryRefundsAndClosureRaces(t *testing.T) {
	f := newFundsFixture(t)
	deadline := f.config.Now().Add(time.Hour)
	q := f.create(t, map[string]any{"is_raising": true, "target_amount": 5, "is_open": true, "deadline_at": deadline})
	f.contribute(t, q.ID, f.one, 2.123, 200)
	expiry := walletpg.NewWithClock(f.db, func() time.Time { return deadline })
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() { defer group.Done(); _, e := expiry.ProcessFundsDue(context.Background(), 1000); errs <- e }()
	}
	group.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	got := f.get(t, q.ID)
	if got.Status != w.FundExpired {
		t.Fatal(got)
	}
	f.balance(t, f.one, "100.000", "0.000")
	f.balance(t, f.creator, "100.000", "0.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.fund_allocations a JOIN wallet_v1.fund_reservations r ON r.id=a.reservation_id WHERE r.fund_id=$1`, q.ID); n != 1 {
		t.Fatal(n)
	}
	packet := f.create(t, map[string]any{"total_amount": 1.001, "amount_of_splits": 2})
	f.claim(t, packet.ID, f.two, 200)
	if _, e := walletpg.NewWithClock(f.db, func() time.Time { return packet.ExpiredAt }).ProcessFundsDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	f.balance(t, f.creator, "99.499", "0.000")
	f.balance(t, f.two, "100.501", "0.000")
}
func TestPGWalletFundsExpiryWinsQueuedFinalContribution(t *testing.T) {
	f := newFundsFixture(t)
	var clock atomic.Int64
	clock.Store(time.Now().UTC().UnixMicro())
	now := func() time.Time { return time.UnixMicro(clock.Load()).UTC() }
	f.config.Now = now
	f.app = backendApp(t, f.db, f.config)
	deadline := now().Add(30 * time.Second)
	q := f.create(t, map[string]any{"is_raising": true, "target_amount": 5, "is_open": true, "deadline_at": deadline})
	f.contribute(t, q.ID, f.one, 1.234, 200)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	af := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocked, pid := atomicBlocker(t, af, "wallet_v1.wallets", f.two.wallet.ID)
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		f.app.ServeHTTP(rec, fixtureRawJSONRequest("POST", "/wallet/wallets/funds/"+q.ID+"/contribute", f.two.token, `{"amount":3.766}`).WithContext(ctx))
		done <- rec.Code
	}()
	waitAtomicBlocked(t, af, pid, "wallet_v1.wallets")
	clock.Store(deadline.UnixMicro())
	expiry := make(chan error, 1)
	go func() { _, e := walletpg.NewWithClock(f.db, now).ProcessFundsDue(ctx, 1000); expiry <- e }()
	if e := blocked.Commit(); e != nil {
		t.Fatal(e)
	}
	if code := <-done; code != 409 {
		t.Fatal("expired queued contribution", code)
	}
	if e := <-expiry; e != nil {
		t.Fatal(e)
	}
	f.balance(t, f.one, "100.000", "0.000")
	f.balance(t, f.two, "100.000", "0.000")
	f.balance(t, f.creator, "100.000", "0.000")
	if f.get(t, q.ID).Status != w.FundExpired {
		t.Fatal("expiry not durable")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1 AND consumed_at IS NOT NULL`, f.two.actor); n != 0 {
		t.Fatal("expired attempt consumed proof", n)
	}
}
func TestPGWalletFundsRollbackAndIdempotentCreation(t *testing.T) {
	f := newFundsFixture(t)
	raw := `{"total_amount":2.001,"currency":"synthetic-funds","amount_of_splits":2}`
	create := func(body string, status int) w.Fund {
		req := fixtureRawJSONRequest("POST", "/wallet/wallets/funds", f.token, body)
		req.Header.Set("Idempotency-Key", "fund-create-replay")
		rec := httptest.NewRecorder()
		f.app.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if status != 200 {
			return w.Fund{}
		}
		return backendDecode[w.Fund](t, rec)
	}
	q := create(raw, 200)
	if again := create(raw, 200); again.ID != q.ID {
		t.Fatal(again)
	}
	create(strings.Replace(raw, "2.001", "2.002", 1), 409)
	f.balance(t, f.creator, "97.999", "2.001")
	if _, e := f.db.Exec(`UPDATE wallet_v1.fund_reservations SET amount=amount+1 WHERE fund_id=$1`, q.ID); e == nil {
		t.Fatal("reservation mutable")
	}
	raising := f.create(t, map[string]any{"is_raising": true, "target_amount": 3.001, "is_open": true})
	f.contribute(t, raising.ID, f.one, 1, 200)
	// Fail after all canonical money effects, on the fund status outbox write.
	query(t, f.db, `CREATE FUNCTION wallet_v1.reject_synthetic_fund()RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='wallet.fund.updated' AND NEW.payload->>'message'='synthetic-fund-rollback' THEN RAISE EXCEPTION 'synthetic fund rollback'; END IF; RETURN NEW;END $$;CREATE TRIGGER reject_synthetic_fund BEFORE INSERT ON wallet_v1.outbox FOR EACH ROW EXECUTE FUNCTION wallet_v1.reject_synthetic_fund()`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS reject_synthetic_fund ON wallet_v1.outbox;DROP FUNCTION IF EXISTS wallet_v1.reject_synthetic_fund()`)
	})
	// Immutable messages require configuring the rollback marker at creation.
	poisoned := f.create(t, map[string]any{"is_raising": true, "target_amount": 2.002, "is_open": true, "message": "synthetic-fund-rollback"})
	f.contribute(t, poisoned.ID, f.two, 2.002, 500)
	f.balance(t, f.two, "100.000", "0.000")
	f.balance(t, f.creator, "97.999", "2.001")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.fund_reservations WHERE fund_id=$1`, poisoned.ID); n != 0 {
		t.Fatal("orphan reservation", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, poisoned.ID); n != 0 {
		t.Fatal("orphan receipt", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1 AND consumed_at IS NOT NULL`, f.two.actor); n != 0 {
		t.Fatal("rollback consumed authorization", n)
	}
	query(t, f.db, `DROP TRIGGER reject_synthetic_fund ON wallet_v1.outbox;DROP FUNCTION wallet_v1.reject_synthetic_fund()`)
	f.contribute(t, poisoned.ID, f.two, 2.002, 200)
	f.balance(t, f.two, "97.998", "0.000")
	f.balance(t, f.creator, "100.001", "2.001")
	backendCall(t, f.app, "POST", "/api/wallets/funds/"+q.ID+"/cancel", f.token, "", 200)
	backendCall(t, f.app, "POST", "/api/wallets/funds/"+raising.ID+"/cancel", f.token, "", 200)
}
func TestPGWalletFundsPINPolicyProofAndRateLimits(t *testing.T) {
	f := newFundsFixture(t)
	q := f.create(t, map[string]any{"is_raising": true, "target_amount": 10, "is_open": true})
	// Missing and disabled canonical policies both deny the amount-only request.
	query(t, f.db, `DELETE FROM wallet_v1.payment_policies WHERE account_id=$1`, f.one.actor)
	f.contribute(t, q.ID, f.one, 1, 503)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,disabled,updated_at)VALUES($1,false,true,now())`, f.one.actor)
	f.contribute(t, q.ID, f.one, 1, 503)
	query(t, f.db, `UPDATE wallet_v1.payment_policies SET disabled=false WHERE account_id=$1`, f.one.actor)
	f.contribute(t, q.ID, f.one, 1, 200)
	// The existing owned PIN lifecycle is the only secret store and verifier.
	pin := newPaymentPINFixture(t)
	pin.enroll(t)
	payer := walletCreate(t, walletpg.New(pin.db), pin.account.ID)
	walletFund(t, pin.db, payer, fundCurrency, "100")
	repo := walletpg.NewWithAuthority(pin.db, pin.guard).WithPaymentAuthorization(pin.pin).WithClock(pin.config.Now)
	ctx, principal := pin.bound(t)
	command := w.FundContributionCommand{ActorID: pin.account.ID, FundID: q.ID, WalletID: payer.ID, Amount: walletAmount(t, "2.001")}
	command.Fingerprint = command.Binding(q)
	if _, e := pin.pin.Atomic().PreparePayment(ctx, principal, "", w.PaymentFundContribute, command.Fingerprint); !errors.Is(e, w.ErrPIN) {
		t.Fatal("PIN omission bypass", e)
	}
	proof, e := pin.pin.Atomic().PreparePayment(ctx, principal, syntheticPaymentPIN, w.PaymentFundContribute, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	changed := command
	changed.Amount = walletAmount(t, "3.001")
	if _, e = repo.ContributeFund(proof, changed); !errors.Is(e, w.ErrPIN) {
		t.Fatal("proof allowed amount substitution", e)
	}
	if _, e = repo.ContributeFund(ctx, command); !errors.Is(e, w.ErrPIN) {
		t.Fatal("missing transaction proof", e)
	}
	query(t, pin.db, `UPDATE wallet_v1.payment_policies SET version=version+1 WHERE account_id=$1`, pin.account.ID)
	if _, e = repo.ContributeFund(proof, command); !errors.Is(e, w.ErrPIN) {
		t.Fatal("stale policy proof", e)
	}
	proof, e = pin.pin.Atomic().PreparePayment(ctx, principal, syntheticPaymentPIN, w.PaymentFundContribute, command.Fingerprint)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.ContributeFund(proof, command); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.ContributeFund(proof, command); !errors.Is(e, w.ErrPIN) {
		t.Fatal("replayed proof", e)
	}
	walletAssertBalance(t, walletpg.New(pin.db), payer.ID, fundCurrency, "97.999", "2.001", "97.999")
	for range 5 {
		if _, e = pin.pin.Atomic().PreparePayment(ctx, principal, "999999", w.PaymentFundContribute, command.Fingerprint); !errors.Is(e, w.ErrPIN) {
			t.Fatal(e)
		}
	}
	if _, e = pin.pin.Atomic().PreparePayment(ctx, principal, syntheticPaymentPIN, w.PaymentFundContribute, command.Fingerprint); !errors.Is(e, w.ErrPIN) {
		t.Fatal("durable lock bypass", e)
	}
	var attempts int
	var until sql.NullTime
	if e = pin.db.QueryRow(`SELECT failed_attempts,locked_until FROM wallet_v1.payment_policies WHERE account_id=$1`, pin.account.ID).Scan(&attempts, &until); e != nil || attempts != 5 || !until.Valid {
		t.Fatal(attempts, until, e)
	}
	backendCall(t, f.app, "POST", "/api/wallets/funds/"+q.ID+"/cancel", f.token, "", 200)
	walletAssertBalance(t, walletpg.New(pin.db), payer.ID, fundCurrency, "100.000", "0.000", "100.000")
}
func TestPGWalletFundsAuthorityAndNotificationProjection(t *testing.T) {
	f := newFundsFixture(t)
	q := f.create(t, map[string]any{"total_amount": 0.003, "amount_of_splits": 1, "recipient_account_ids": []string{f.one.actor}})
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.one.actor, []authority.Rule{{Pattern: w.PermissionTransactions}}, nil); e != nil {
		t.Fatal(e)
	}
	f.claim(t, q.ID, f.one, 403)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.one.actor, []authority.Rule{{Pattern: w.PermissionFunds}}, nil); e != nil {
		t.Fatal(e)
	}
	f.claim(t, q.ID, f.one, 200)
	projector := notificationpg.New(f.db, nil, "synthetic-funds-events")
	notificationDrain(t, func() (int, error) { return projector.ProjectWallet(context.Background(), 100) })
	var projected int
	if e := f.db.QueryRow(`SELECT count(*) FROM notification_v1.receipts n JOIN wallet_v1.outbox o ON o.event_id=n.source_id WHERE n.source='wallet' AND n.app_id='synthetic-funds-events' AND o.aggregate_id=$1 AND o.event_type LIKE 'wallet.fund.%'`, q.ID).Scan(&projected); e != nil || projected != 2 {
		t.Fatal(projected, e)
	}
}
func TestPGWalletFundsRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "wallet-funds-state.json")
	type state struct {
		Pending, Complete, Cancelled, Creator, Payer, Receipt string
		Expires                                               time.Time
	}
	if phase == "after" {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var saved state
		if e = json.Unmarshal(raw, &saved); e != nil {
			t.Fatal(e)
		}
		repo := walletpg.New(db)
		f, e := repo.Fund(context.Background(), "", saved.Pending)
		if e != nil || f.Status != w.FundPartial || f.RaisedAmount.String() != "2.001" {
			t.Fatal(f, e)
		}
		walletAssertBalance(t, repo, saved.Payer, fundCurrency, "96.998", "2.001", "96.998")
		complete, e := repo.Fund(context.Background(), "", saved.Complete)
		if e != nil || complete.Status != w.FundCompleted {
			t.Fatal(complete, e)
		}
		cancelled, e := repo.Fund(context.Background(), "", saved.Cancelled)
		if e != nil || cancelled.Status != w.FundRefunded {
			t.Fatal(cancelled, e)
		}
		tr, e := repo.GetTransaction(context.Background(), saved.Receipt)
		if e != nil || tr.Status != w.Confirmed {
			t.Fatal(tr, e)
		}
		if _, e = repo.WithClock(func() time.Time { return saved.Expires }).ProcessFundsDue(context.Background(), 1000); e != nil {
			t.Fatal(e)
		}
		walletAssertBalance(t, repo, saved.Payer, fundCurrency, "98.999", "0.000", "98.999")
		walletAssertBalance(t, repo, saved.Creator, fundCurrency, "101.001", "0.000", "101.001")
		if _, e = repo.ProcessFundsDue(context.Background(), 1000); e != nil {
			t.Fatal(e)
		}
		walletAssertBalance(t, repo, saved.Payer, fundCurrency, "98.999", "0.000", "98.999")
		return
	}
	f := newFundsFixture(t)
	pending := f.create(t, map[string]any{"is_raising": true, "target_amount": 10, "is_open": true, "expiration_hours": 168})
	f.contribute(t, pending.ID, f.one, 2.001, 200)
	complete := f.create(t, map[string]any{"is_raising": true, "target_amount": 1.001, "is_open": true})
	receipt := backendDecode[w.Transaction](t, f.contribute(t, complete.ID, f.one, 1.001, 200))
	cancelled := f.create(t, map[string]any{"is_raising": true, "target_amount": 5, "is_open": true})
	f.contribute(t, cancelled.ID, f.one, 3, 200)
	backendCall(t, f.app, "POST", "/wallet/wallets/funds/"+cancelled.ID+"/cancel", f.token, "", 200)
	raw, _ := json.Marshal(state{pending.ID, complete.ID, cancelled.ID, f.creator.wallet.ID, f.one.wallet.ID, receipt.ID, pending.ExpiredAt})
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestPGWalletFundsFixedRandomAndInputBounds(t *testing.T) {
	f := newFundsFixture(t)
	q := f.create(t, map[string]any{"is_raising": true, "target_amount": 0.006, "is_open": false, "recipient_account_ids": []string{f.one.actor, f.two.actor}, "amount_of_splits": 2, "contribution_type": 1, "contribution_amount": 0.003})
	first := backendDecode[w.Transaction](t, f.contribute(t, q.ID, f.one, 0, 200))
	if first.Amount.String() != "0.003" || first.Status != w.Frozen {
		t.Fatal(first)
	}
	last := backendDecode[w.Transaction](t, f.contribute(t, q.ID, f.two, 999, 200))
	if last.Amount.String() != "0.003" || last.Status != w.Confirmed {
		t.Fatal(last)
	}
	f.balance(t, f.creator, "100.006", "0.000")
	packet := f.create(t, map[string]any{"total_amount": 1.001, "amount_of_splits": 3, "split_type": 1})
	total := walletAmount(t, "0")
	for _, p := range []fundPerson{f.one, f.two, f.three} {
		tr := backendDecode[w.Transaction](t, f.claim(t, packet.ID, p, 200))
		if tr.Amount.Sign() <= 0 {
			t.Fatal(tr)
		}
		var e error
		total, e = total.Add(tr.Amount)
		if e != nil {
			t.Fatal(e)
		}
	}
	if total.String() != "1.001" || f.get(t, packet.ID).RemainingAmount.Sign() != 0 {
		t.Fatal(total)
	}
	for _, body := range []string{`{"total_amount":0.002,"amount_of_splits":3,"currency":"synthetic-funds"}`, `{"total_amount":1,"amount_of_splits":0,"currency":"synthetic-funds"}`, `{"is_raising":true,"target_amount":0,"amount_of_splits":3,"currency":"synthetic-funds"}`, `{"is_raising":true,"target_amount":1,"contribution_type":1,"contribution_amount":2,"amount_of_splits":3,"currency":"synthetic-funds"}`} {
		backendCall(t, f.app, "POST", "/wallet/wallets/funds", f.token, body, 400)
	}
}
