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
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync"
	"testing"
	"time"
)

type streamAwardFixture struct {
	streamFixture
	commerce                                   *commercepg.Repository
	wallets                                    *walletpg.Repository
	beneficiary, donor, editorWallet, treasury wallet.Wallet
	treasuryActor                              string
	options                                    livestreampg.AwardConfig
}

func newStreamAwardFixture(t *testing.T) streamAwardFixture {
	t.Helper()
	f := streamAwardFixture{streamFixture: newStreamFixture(t)}
	var installed bool
	if e := f.db.QueryRow(`SELECT to_regclass('commerce_v1.orders') IS NOT NULL AND to_regclass('wallet_v1.wallets') IS NOT NULL`).Scan(&installed); e != nil || !installed {
		t.Fatal("canonical commerce/wallet schema missing from disposable harness", e)
	}
	query(t, f.db, livestreampg.AwardsSchema)
	f.treasuryActor = authorityUUID(t)
	if e := authoritypg.SeedSynthetic(f.ctx, f.db, f.treasuryActor, "synthetic_stream_treasury_"+strings.ReplaceAll(f.treasuryActor, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	// Trusted fixture provisioning only: canonical wallets and explicit synthetic
	// balances; no runtime enrollment, external payment or production credentials.
	f.wallets = walletpg.New(f.db)
	f.beneficiary = walletCreate(t, f.wallets, f.owner)
	f.donor = walletCreate(t, f.wallets, f.third)
	f.editorWallet = walletCreate(t, f.wallets, f.other)
	f.treasury = walletCreate(t, f.wallets, f.treasuryActor)
	f.options = livestreampg.AwardConfig{PointsCurrency: "synthetic-stream-points", TreasuryAccountID: f.treasuryActor, TreasuryWalletID: f.treasury.ID, MaxAmount: walletAmount(t, "100"), MaxOrdersPerAccountStream: 20, MaxAwardsPerStream: 100, OrderTTL: time.Hour}
	for _, w := range []wallet.Wallet{f.beneficiary, f.donor, f.editorWallet} {
		walletFund(t, f.db, w, f.options.PointsCurrency, "100.000")
	}
	f.commerce = commercepg.New(f.db, f.guard, commercepg.Adapters{})
	if e := f.repo.WithAwards(f.options, f.commerce); e != nil {
		t.Fatal(e)
	}
	f.commerce.WithLiveStreamAwards(f.repo)
	if e := f.repo.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func streamAward(t *testing.T, f streamAwardFixture, ctx context.Context, actor, sid, amount, key string) live.AwardOrderResponse {
	t.Helper()
	points, e := live.ParseAwardPoints(amount)
	if e != nil {
		t.Fatal(e)
	}
	message := "Synthetic paid award"
	o, e := f.repo.CreateAwardOrder(ctx, actor, sid, key, live.AwardRequest{Amount: points, Message: &message})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func paidStreamAward(t *testing.T, f streamAwardFixture, sid, amount, key string) (live.AwardOrderResponse, commerce.Order) {
	t.Helper()
	o := streamAward(t, f, f.viewerCtx, f.third, sid, amount, key)
	paid, e := f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID)
	if e != nil || paid.Status != commerce.Finished {
		t.Fatal(paid, e)
	}
	return o, paid
}
func awardDue(t *testing.T, f streamAwardFixture) {
	t.Helper()
	if _, e := f.repo.ProcessAwardsDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
}
func awardState(t *testing.T, f streamAwardFixture, order string) string {
	t.Helper()
	var state string
	if e := f.db.QueryRow(`SELECT payout_state FROM owned_livestream_award_receipts WHERE order_id=$1::uuid`, order).Scan(&state); e != nil {
		t.Fatal(e)
	}
	return state
}

func TestPGLiveStreamAwardsPaidLifecycleAndConservation(t *testing.T) {
	f := newStreamAwardFixture(t)
	foreign := f.options
	foreign.TreasuryAccountID = f.owner
	if e := f.repo.WithAwards(foreign, f.commerce); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.AwardsReady(f.ctx); !errors.Is(e, live.ErrAwardUnavailable) {
		t.Fatal("foreign treasury wallet accepted", e)
	}
	if e := f.repo.WithAwards(f.options, f.commerce); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1::uuid,'disable_account',clock_timestamp())`, f.treasuryActor)
	if e := f.repo.AwardsReady(f.ctx); !errors.Is(e, live.ErrAwardUnavailable) {
		t.Fatal("active treasury punishment accepted", e)
	}
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1::uuid AND kind='disable_account'`, f.treasuryActor)
	if e := f.repo.AwardsReady(f.ctx); e != nil {
		t.Fatal(e)
	}
	s := streamCreate(t, f.streamFixture, "awards-paid")
	streamNative(t, f.streamFixture, s.ID)
	base := "/api/livestreams/" + s.ID + "/awards"
	backendJSONCall(t, f.handler, "POST", base, "", map[string]any{"amount": 10}, 401)
	for _, body := range []any{map[string]any{"amount": 0.001}, map[string]any{"amount": 10, "payee_wallet_id": f.donor.ID}, map[string]any{"amount": "10"}} {
		backendJSONCall(t, f.handler, "POST", base, f.thirdToken, body, 400)
	}
	o := streamAward(t, f, f.viewerCtx, f.third, s.ID, "10.50", "award-idem")
	repeated := streamAward(t, f, f.viewerCtx, f.third, s.ID, "10.50", "award-idem")
	if repeated.OrderID != o.OrderID {
		t.Fatal("duplicate order")
	}
	items, total, e := f.repo.ListAwards(f.ctx, "", s.ID, 20, 0, false)
	if e != nil || total != 0 || len(items) != 0 {
		t.Fatal("unpaid order became award", items, total, e)
	}
	f.commerce.WithLiveStreamAwards(nil)
	if _, e = f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID); !errors.Is(e, commerce.ErrUnavailable) {
		t.Fatal(e)
	}
	f.commerce.WithLiveStreamAwards(f.repo)
	h := (&commerce.Service{Store: f.commerce, Identity: commerceTestIdentity{f.third, f.viewerCtx}, Payments: walletPIN{}}).Handler()
	backendJSONCall(t, h, "POST", "/api/orders/"+o.OrderID+"/pay", f.thirdToken, map[string]any{"pin_code": "wrong", "payer_wallet_id": f.donor.ID}, 403)
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	w := backendJSONCall(t, h, "POST", "/api/orders/"+o.OrderID+"/pay", f.thirdToken, map[string]any{"pin_code": "synthetic-proof", "payer_wallet_id": f.donor.ID}, 200)
	var paid commerce.Order
	if e = json.Unmarshal(w.Body.Bytes(), &paid); e != nil || paid.Status != commerce.Finished {
		t.Fatal(paid, e)
	}
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "89.500", "0.000", "89.500")
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "1.050", "9.450", "1.050")
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	items, total, e = f.repo.ListAwards(f.ctx, "", s.ID, 20, 0, false)
	if e != nil || total != 1 || len(items) != 1 || items[0].Amount.String() != "10.500" || items[0].Attitude != 1 || items[0].AccountID != f.third {
		t.Fatal(items, total, e)
	}
	leaders, e := f.repo.AwardLeaderboard(f.ctx, "", s.ID, 10)
	if e != nil || len(leaders) != 1 || leaders[0].Account == nil || leaders[0].Account.ID != f.third || leaders[0].AwardCount != 1 {
		t.Fatal(leaders, e)
	}
	stream, e := f.repo.Get(f.ctx, "", s.ID)
	if e != nil || stream.TotalAwardScore.String() != "10.500" || stream.DistributedAwardAmount.Sign() != 0 {
		t.Fatal(stream, e)
	}
	if _, e = f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID); e != nil {
		t.Fatal(e)
	}
	awardDue(t, f)
	if awardState(t, f, o.OrderID) != "held" {
		t.Fatal("paid before end")
	}
	if e = f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}
	awardDue(t, f)
	awardDue(t, f)
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "1.050", "0.000", "1.050")
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "109.450", "0.000", "109.450")
	if awardState(t, f, o.OrderID) != "released" {
		t.Fatal("payout state")
	}
	stream, e = f.repo.Get(f.ctx, "", s.ID)
	if e != nil || stream.DistributedAwardAmount.String() != "10.500" {
		t.Fatal(stream, e)
	}
	var canonical int
	if e = f.db.QueryRow(`SELECT count(*) FROM wallet_v1.transactions t JOIN owned_livestream_award_receipts r ON t.id IN(r.transaction_id,r.reserve_transaction_id) WHERE r.order_id=$1::uuid AND t.status=2`, o.OrderID).Scan(&canonical); e != nil || canonical != 2 {
		t.Fatal("canonical settlement count", canonical, e)
	}
}

func TestPGLiveStreamAwardsFulfillmentRollbackAndReplay(t *testing.T) {
	f := newStreamAwardFixture(t)
	s := streamCreate(t, f.streamFixture, "awards-rollback")
	streamNative(t, f.streamFixture, s.ID)
	o := streamAward(t, f, f.viewerCtx, f.third, s.ID, "10", "rollback")

	unpaid, e := f.commerce.GetOrder(f.viewerCtx, f.third, o.OrderID, false)
	if e != nil {
		t.Fatal(e)
	}
	fakeTransaction := authorityUUID(t)
	unpaid.TransactionID = &fakeTransaction
	tx, e := f.db.BeginTx(f.viewerCtx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.FulfillLiveStreamAward(f.viewerCtx, f.db, tx, f.third, unpaid, time.Now().UTC()); !errors.Is(e, live.ErrForbidden) {
		tx.Rollback()
		t.Fatal("unsettled callback created award", e)
	}
	tx.Rollback()
	constraint := "synthetic_award_fail_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_audit ADD CONSTRAINT `+constraint+` CHECK(NOT(stream_id='`+s.ID+`'::uuid AND action='livestreams.award.fulfilled'))NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_livestream_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID); e == nil {
		t.Fatal("injected fulfillment audit passed")
	}
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "0.000", "0.000", "0.000")
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_award_receipts WHERE order_id=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `ALTER TABLE owned_livestream_audit DROP CONSTRAINT `+constraint)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_award_receipts WHERE order_id=$1::uuid`, o.OrderID); n != 1 {
		t.Fatal(n)
	}
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "90.000", "0.000", "90.000")
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "1.000", "9.000", "1.000")
}

func TestPGLiveStreamAwardsFreshAuthorityAndNOWAITRollback(t *testing.T) {
	f := newStreamAwardFixture(t)
	s := streamCreate(t, f.streamFixture, "awards-lock")
	streamNative(t, f.streamFixture, s.ID)
	o := streamAward(t, f, f.viewerCtx, f.third, s.ID, "10", "owner-lock")
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.owner}
	blocker, _ := atomicBlocker(t, base, "owned_authority_accounts", f.owner)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET version=version+1 WHERE id=$1::uuid`, f.owner); e != nil {
		t.Fatal(e)
	}
	if _, e := f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID); !errors.Is(e, commerce.ErrRetry) {
		t.Fatal("secondary account did not fail retryably", e)
	}
	h := (&commerce.Service{Store: f.commerce, Identity: commerceTestIdentity{f.third, f.viewerCtx}, Payments: walletPIN{}}).Handler()
	response := backendJSONCall(t, h, "POST", "/api/orders/"+o.OrderID+"/pay", f.thirdToken, map[string]any{"pin_code": "synthetic-proof", "payer_wallet_id": f.donor.ID}, 409)
	if !strings.Contains(response.Body.String(), "ORDER_RETRY_REQUIRED") {
		t.Fatal("retryable transport code lost", response.Body.String())
	}

	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	if e := blocker.Rollback(); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.third, []authority.Rule{{Pattern: "*"}, {Pattern: "livestreams.awards", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("withdrawn award scope accepted", e)
	}
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal(n)
	}
}

func TestPGLiveStreamAwardsOwnerBindingAndProviderIndependentPayout(t *testing.T) {
	f := newStreamAwardFixture(t)
	s := streamCreate(t, f.streamFixture, "awards-owner")
	streamNative(t, f.streamFixture, s.ID)
	unpaid := streamAward(t, f, f.viewerCtx, f.third, s.ID, "10", "before-owner-change")
	paid, _ := paidStreamAward(t, f, s.ID, "10", "earned")
	// Trusted fixture ownership transfer under the canonical publisher row. A
	// payment pending across it is denied; a settled receipt retains its owner.
	query(t, f.db, `UPDATE owned_publishers SET account_id=$2::uuid WHERE id=$1::uuid`, f.pub.ID, f.other)
	if _, e := f.commerce.PayOrder(f.viewerCtx, f.third, unpaid.OrderID, &f.donor.ID); !errors.Is(e, commerce.ErrConflict) {
		t.Fatal(e)
	}
	f.provider.failDelete = true
	if e := f.repo.End(f.editorCtx, f.other, s.ID, false); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if _, e := f.repo.ProcessDue(f.ctx, 100); !errors.Is(e, live.ErrProvider) {
		t.Fatal("expected independent provider cleanup failure", e)
	}
	if awardState(t, f, paid.OrderID) != "released" {
		t.Fatal("provider outage blocked monetary obligation")
	}
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "109.000", "0.000", "109.000")
	walletAssertBalance(t, f.wallets, f.editorWallet.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "1.000", "0.000", "1.000")
}

type heldAwardHook struct {
	target   *livestreampg.Repository
	prepared chan int
	release  chan struct{}
}

func (h heldAwardHook) PrepareLiveStreamAward(ctx context.Context, db *sql.DB, tx *sql.Tx, a string, o commerce.Order, payer string, now time.Time) error {
	if e := h.target.PrepareLiveStreamAward(ctx, db, tx, a, o, payer, now); e != nil {
		return e
	}
	var pid int
	if e := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		return e
	}
	h.prepared <- pid
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h heldAwardHook) FulfillLiveStreamAward(ctx context.Context, db *sql.DB, tx *sql.Tx, a string, o commerce.Order, now time.Time) error {
	return h.target.FulfillLiveStreamAward(ctx, db, tx, a, o, now)
}
func TestPGLiveStreamAwardsEndDuringPaymentAndUnsupportedRefund(t *testing.T) {
	f := newStreamAwardFixture(t)
	s := streamCreate(t, f.streamFixture, "awards-end")
	streamNative(t, f.streamFixture, s.ID)
	o := streamAward(t, f, f.viewerCtx, f.third, s.ID, "10", "payment-versus-end")
	hook := heldAwardHook{f.repo, make(chan int, 1), make(chan struct{})}
	f.commerce.WithLiveStreamAwards(hook)
	var releaseOnce sync.Once
	releasePayment := func() { releaseOnce.Do(func() { close(hook.release) }) }
	t.Cleanup(releasePayment)
	paid := make(chan error, 1)
	ending := make(chan error, 1)
	go func() { _, e := f.commerce.PayOrder(f.viewerCtx, f.third, o.OrderID, &f.donor.ID); paid <- e }()
	var pid int
	select {
	case pid = <-hook.prepared:
	case err := <-paid:
		t.Fatal("payment failed before prepared latch", err)
	case <-f.ctx.Done():
		t.Fatal("payment preparation timed out")
	}
	go func() { ending <- f.repo.End(f.ownerCtx, f.owner, s.ID, false) }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.owner}, pid, "owned_authority_accounts")
	releasePayment()
	if e := <-paid; e != nil {
		t.Fatal(e)
	}
	if e := <-ending; e != nil {
		t.Fatal(e)
	}
	var source, reserve string
	if e := f.db.QueryRow(`SELECT transaction_id::text,reserve_transaction_id::text FROM owned_livestream_award_receipts WHERE order_id=$1::uuid`, o.OrderID).Scan(&source, &reserve); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var payoutError error
	go func() { defer wg.Done(); _, payoutError = f.repo.ProcessAwardsDue(f.ctx, 100) }()
	for _, id := range []string{source, reserve} {
		if _, e := f.wallets.Transition(f.ctx, id, wallet.Owner{AccountID: f.treasuryActor}, wallet.Reject); e == nil {
			t.Fatal("generic refund modified managed award funds")
		}
	}
	wg.Wait()
	if payoutError != nil {
		t.Fatal(payoutError)
	}
	awardDue(t, f)
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "90.000", "0.000", "90.000")
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "109.000", "0.000", "109.000")
}

func TestPGLiveStreamAwardsOpposingActorsAndStreams(t *testing.T) {
	f := newStreamAwardFixture(t)
	first := streamCreate(t, f.streamFixture, "opposing-first")
	streamNative(t, f.streamFixture, first.ID)
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1::uuid,5,clock_timestamp()-interval '1 second')ON CONFLICT(account_id)DO UPDATE SET total=5,starts_at=EXCLUDED.starts_at`, f.other)
	name := "synthetic-award-other-" + strings.ReplaceAll(f.other, "-", "")
	nick := "Synthetic opposing stream"
	pub, e := f.accounts.CreatePublisher(f.editorCtx, f.other, accounts.PublisherPatch{Name: &name, Nick: &nick})
	if e != nil {
		t.Fatal(e)
	}
	title := "Other synthetic stream"
	second, e := f.repo.Create(f.editorCtx, f.other, pub.Name, "opposing-second", live.Patch{Title: &title})
	if e != nil {
		t.Fatal(e)
	}
	yes := true
	if _, e = f.repo.Start(f.editorCtx, f.other, second.ID, live.StartRequest{NoIngress: &yes}); e != nil {
		t.Fatal(e)
	}
	a := streamAward(t, f, f.ownerCtx, f.owner, second.ID, "10", "owner-to-other")
	b := streamAward(t, f, f.editorCtx, f.other, first.ID, "10", "other-to-owner")
	gate := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-gate
		_, e := f.commerce.PayOrder(f.ownerCtx, f.owner, a.OrderID, &f.beneficiary.ID)
		done <- e
	}()
	go func() {
		<-gate
		_, e := f.commerce.PayOrder(f.editorCtx, f.other, b.OrderID, &f.editorWallet.ID)
		done <- e
	}()
	close(gate)
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			if e != nil && !errors.Is(e, commerce.ErrRetry) {
				t.Fatal(e)
			}
		case <-f.ctx.Done():
			t.Fatal("opposing account/wallet lock inversion")
		}
	}
	for _, item := range []struct {
		ctx                  context.Context
		actor, order, wallet string
	}{{f.ownerCtx, f.owner, a.OrderID, f.beneficiary.ID}, {f.editorCtx, f.other, b.OrderID, f.editorWallet.ID}} {
		if _, e = f.commerce.PayOrder(item.ctx, item.actor, item.order, &item.wallet); e != nil {
			t.Fatal(e)
		}
	}
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_award_receipts WHERE order_id IN($1::uuid,$2::uuid)`, a.OrderID, b.OrderID); n != 2 {
		t.Fatal(n)
	}
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "2.000", "18.000", "2.000")
}

func TestPGLiveStreamAwardsExpiryCapsAndPayoutRollback(t *testing.T) {
	f := newStreamAwardFixture(t)
	f.options.MaxOrdersPerAccountStream = 2
	if e := f.repo.WithAwards(f.options, f.commerce); e != nil {
		t.Fatal(e)
	}
	s := streamCreate(t, f.streamFixture, "awards-boundaries")
	streamNative(t, f.streamFixture, s.ID)
	o, _ := paidStreamAward(t, f, s.ID, "0.01", "minimum")
	rows, _, e := f.repo.ListAwards(f.ctx, "", s.ID, 100, 0, true)
	if e != nil || len(rows) != 0 {
		t.Fatal("zero-second source highlight remained active", rows, e)
	}
	o2 := streamAward(t, f, f.viewerCtx, f.third, s.ID, "1", "expired-order")
	query(t, f.db, `UPDATE commerce_v1.orders SET expires_at=clock_timestamp()-interval '1 microsecond',document=jsonb_set(document,'{expired_at}',to_jsonb(clock_timestamp()-interval '1 microsecond')) WHERE id=$1::uuid`, o2.OrderID)
	if _, e = f.commerce.PayOrder(f.viewerCtx, f.third, o2.OrderID, &f.donor.ID); !errors.Is(e, commerce.ErrConflict) {
		t.Fatal(e)
	}
	points, _ := live.ParseAwardPoints("1")
	if _, e = f.repo.CreateAwardOrder(f.viewerCtx, f.third, s.ID, "capped", live.AwardRequest{Amount: points}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("lifetime order cap accepted an additional order", e)
	}
	if e = f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}

	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1::uuid`, f.owner)
	if _, e = f.repo.ProcessAwardsDue(f.ctx, 100); e == nil {
		t.Fatal("disabled beneficiary was credited")
	}
	var reason string
	if e = f.db.QueryRow(`SELECT reason FROM owned_livestream_award_receipts WHERE order_id=$1::uuid`, o.OrderID).Scan(&reason); e != nil || reason != "beneficiary_unavailable" {
		t.Fatal(reason, e)
	}
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false,version=version+1 WHERE id=$1::uuid`, f.owner)
	query(t, f.db, `UPDATE owned_livestream_award_receipts SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE order_id=$1::uuid`, o.OrderID)
	constraint := "synthetic_award_payout_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_audit ADD CONSTRAINT `+constraint+` CHECK(NOT(stream_id='`+s.ID+`'::uuid AND action='livestreams.award.distributed'))NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_livestream_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e = f.repo.ProcessAwardsDue(f.ctx, 100); e == nil {
		t.Fatal("payout audit failure passed")
	}
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "0.001", "0.009", "0.001")
	query(t, f.db, `ALTER TABLE owned_livestream_audit DROP CONSTRAINT `+constraint)
	query(t, f.db, `UPDATE owned_livestream_award_receipts SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE order_id=$1::uuid`, o.OrderID)
	awardDue(t, f)
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "100.009", "0.000", "100.009")
	if _, e = f.repo.ProcessAwardsDue(f.ctx, 101); !errors.Is(e, live.ErrInvalid) {
		t.Fatal(e)
	}
}

func TestPGLiveStreamAwardsPayoutRestartPersistence(t *testing.T) {
	database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-livestream-award-restart.json")
	type savedState struct{ Order, Stream, OwnerWallet, TreasuryWallet, TreasuryActor, Currency string }
	var saved savedState
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newStreamAwardFixture(t)
		s := streamCreate(t, f.streamFixture, "award-restart")
		streamNative(t, f.streamFixture, s.ID)
		o, _ := paidStreamAward(t, f, s.ID, "10", "restart")
		if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
			t.Fatal(e)
		}
		saved = savedState{o.OrderID, s.ID, f.beneficiary.ID, f.treasury.ID, f.treasuryActor, f.options.PointsCurrency}
		raw, _ := json.Marshal(saved)
		if e := os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatal("missing synthetic award state", e)
	}
	f := newStreamAwardFixture(t)
	query(t, f.db, `UPDATE owned_livestream_award_receipts SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE order_id=$1::uuid`, saved.Order)
	// A different configured treasury cannot process another binding.
	if _, e = f.repo.ProcessAwardsDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if awardState(t, f, saved.Order) != "held" {
		t.Fatal("foreign treasury processed reserved funds")
	}
	f.options.TreasuryAccountID = saved.TreasuryActor
	f.options.TreasuryWalletID = saved.TreasuryWallet
	f.options.PointsCurrency = saved.Currency
	if e = f.repo.WithAwards(f.options, f.commerce); e != nil {
		t.Fatal(e)
	}

	offline, e := livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, nil, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if e = offline.WithAwards(f.options, f.commerce); e != nil {
		t.Fatal(e)
	}
	if e = offline.AwardsReady(f.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = offline.ProcessDue(f.ctx, 100); !errors.Is(e, live.ErrUnconfigured) {
		t.Fatal("provider-independent payout did not retain explicit RTC unavailable error", e)
	}
	walletAssertBalance(t, f.wallets, saved.OwnerWallet, saved.Currency, "109.000", "0.000", "109.000")
	walletAssertBalance(t, f.wallets, saved.TreasuryWallet, saved.Currency, "1.000", "0.000", "1.000")
	if awardState(t, f, saved.Order) != "released" {
		t.Fatal("restart payout")
	}
	maintenance, e := livestreampg.NewAwardMaintenance(f.db, f.commerce, livestreampg.AwardMaintenanceConfig{PointsCurrency: saved.Currency, TreasuryAccountID: saved.TreasuryActor, TreasuryWalletID: saved.TreasuryWallet})
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := any(maintenance).(live.Repository); ok {
		t.Fatal("payout-only object exposed stream operations")
	}
	if _, ok := any(maintenance).(live.AwardRepository); ok {
		t.Fatal("payout-only object exposed new award orders")
	}
	if e = maintenance.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = maintenance.ProcessDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}

	awardDue(t, f)
	walletAssertBalance(t, f.wallets, saved.OwnerWallet, saved.Currency, "109.000", "0.000", "109.000")
}

func TestPGLiveStreamAwardsHTTPStrictRequestTransport(t *testing.T) {
	f := newStreamAwardFixture(t)
	s := streamCreate(t, f.streamFixture, "awards-transport")
	streamNative(t, f.streamFixture, s.ID)
	path := "/api/livestreams/" + s.ID + "/awards"
	req := fixtureJSONRequest(t, "POST", path, f.thirdToken, map[string]any{"amount": 10})
	req.Header.Set("Idempotency-Key", "same")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	req = fixtureNoBodyRequest("POST", path, f.thirdToken)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
	streamGetCall(t, f.streamFixture, path+"?limit=101", f.token, 400)
	streamGetCall(t, f.streamFixture, path+"/leaderboard?limit=3", "", 200)
}

func TestPGLiveStreamAwardRefundReleasesReserveAndReversesCapture(t *testing.T) {
	f := newStreamAwardFixture(t)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.owner)
	root, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	rootCtx := f.guard.Bind(f.ctx, root)
	s := streamCreate(t, f.streamFixture, "award-refund")
	streamNative(t, f.streamFixture, s.ID)
	o, _ := paidStreamAward(t, f, s.ID, "10", "refund-source")
	var purchase string
	if e = f.db.QueryRow(`SELECT id::text FROM owned_livestream_award_purchases WHERE order_id=$1::uuid`, o.OrderID).Scan(&purchase); e != nil {
		t.Fatal(e)
	}
	refund, e := f.repo.RequestAwardRefund(rootCtx, f.owner, s.ID, purchase, live.AwardRefundRequest{Reason: "operator reconciliation"})
	if e != nil || refund.State != "refund_pending" || refund.PurchaseID != purchase {
		t.Fatal(refund, e)
	}
	items, total, e := f.repo.ListAwards(f.ctx, "", s.ID, 20, 0, false)
	if e != nil || total != 0 || len(items) != 0 {
		t.Fatal("pending refund remained publicly counted", items, total, e)
	}
	constraint := "synthetic_refund_phase_" + strings.ReplaceAll(purchase, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_award_receipts ADD CONSTRAINT `+constraint+` CHECK(NOT(purchase_id='`+purchase+`'::uuid AND payout_state='refunded'))NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE owned_livestream_award_receipts DROP CONSTRAINT IF EXISTS ` + constraint)
	})
	if _, e := f.repo.ProcessAwardsDue(f.ctx, 100); e == nil {
		t.Fatal("second refund phase failure was not injected")
	}
	if state := awardState(t, f, o.OrderID); state != "refund_ready" {
		t.Fatal("durable refund checkpoint lost", state)
	}
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "90.000", "0.000", "90.000")
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "10.000", "0.000", "10.000")
	query(t, f.db, `ALTER TABLE owned_livestream_award_receipts DROP CONSTRAINT `+constraint)
	query(t, f.db, `UPDATE owned_livestream_award_receipts SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE purchase_id=$1::uuid`, purchase)
	if n, e := f.repo.ProcessAwardsDue(f.ctx, 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if state := awardState(t, f, o.OrderID); state != "refunded" {
		t.Fatal(state)
	}
	walletAssertBalance(t, f.wallets, f.donor.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	walletAssertBalance(t, f.wallets, f.treasury.ID, f.options.PointsCurrency, "0.000", "0.000", "0.000")
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
	var originalStatus, distributionStatus int
	if e = f.db.QueryRow(`SELECT o.status,d.status FROM owned_livestream_award_receipts r JOIN commerce_v1.orders o ON o.id=r.order_id JOIN commerce_v1.orders d ON d.id=r.distribution_order_id WHERE r.purchase_id=$1::uuid`, purchase).Scan(&originalStatus, &distributionStatus); e != nil || originalStatus != int(commerce.Cancelled) || distributionStatus != int(commerce.Cancelled) {
		t.Fatal(originalStatus, distributionStatus, e)
	}
	replayed, e := f.repo.RequestAwardRefund(rootCtx, f.owner, s.ID, purchase, live.AwardRefundRequest{Reason: "operator reconciliation"})
	if e != nil || replayed.State != "refunded" || replayed.RefundedAt == nil {
		t.Fatal(replayed, e)
	}
}

func TestPGLiveStreamAwardCapturesRealmWalletBeneficiary(t *testing.T) {
	f := newStreamAwardFixture(t)
	s := streamCreate(t, f.streamFixture, "realm-award")
	streamNative(t, f.streamFixture, s.ID)
	realm := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_realms(id,slug,name,account_id,created_at,updated_at)VALUES($1::uuid,$1::text,'Synthetic livestream realm',$2::uuid,clock_timestamp(),clock_timestamp())`, realm, f.owner)
	realmWallet, e := f.wallets.CreateWallet(f.ctx, wallet.Owner{RealmID: realm}, "Synthetic livestream realm wallet")
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_publishers SET type=1,account_id=NULL,realm_id=$2::uuid,updated_at=clock_timestamp() WHERE id=$1::uuid`, f.pub.ID, realm)
	o, _ := paidStreamAward(t, f, s.ID, "10", "realm-beneficiary")
	var capturedRealm, capturedWallet string
	if e = f.db.QueryRow(`SELECT beneficiary_realm_id::text,beneficiary_wallet_id::text FROM owned_livestream_award_purchases WHERE order_id=$1::uuid`, o.OrderID).Scan(&capturedRealm, &capturedWallet); e != nil || capturedRealm != realm || capturedWallet != realmWallet.ID {
		t.Fatal(capturedRealm, capturedWallet, e)
	}
	if e = f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}
	// A later realm ownership transfer cannot redirect or strand the realm's
	// already-earned award; the captured realm wallet remains the beneficiary.
	query(t, f.db, `UPDATE owned_realms SET account_id=$2::uuid,updated_at=clock_timestamp() WHERE id=$1::uuid`, realm, f.other)
	awardDue(t, f)
	walletAssertBalance(t, f.wallets, realmWallet.ID, f.options.PointsCurrency, "9.000", "0.000", "9.000")
	walletAssertBalance(t, f.wallets, f.beneficiary.ID, f.options.PointsCurrency, "100.000", "0.000", "100.000")
}

func TestPGLiveStreamAwardRealtimeUsesDurableCurrentAudienceOutbox(t *testing.T) {
	f := newStreamChatFixture(t)
	if e := f.repo.WithChat(livestreampg.ChatConfig{MaxMessagesPerStream: 1, MaxEventsPerStream: 1, MaxAttempts: 3, EventTTL: time.Hour, SendTimeout: time.Second}); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, livestreampg.AwardsSchema)
	treasuryActor := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(f.ctx, f.db, treasuryActor, "synthetic_realtime_award_treasury_"+strings.ReplaceAll(treasuryActor, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	wallets := walletpg.New(f.db)
	donor := walletCreate(t, wallets, f.third)
	_ = walletCreate(t, wallets, f.owner)
	treasury := walletCreate(t, wallets, treasuryActor)
	currency := "synthetic-realtime-awards"
	walletFund(t, f.db, donor, currency, "100.000")
	maximum, _ := wallet.ParseAmount("100")
	commerceRepo := commercepg.New(f.db, f.guard, commercepg.Adapters{})
	options := livestreampg.AwardConfig{PointsCurrency: currency, TreasuryAccountID: treasuryActor, TreasuryWalletID: treasury.ID, MaxAmount: maximum, MaxOrdersPerAccountStream: 20, MaxAwardsPerStream: 100, OrderTTL: time.Hour, RealtimePackets: true}
	if e := f.repo.WithAwards(options, commerceRepo); e != nil {
		t.Fatal(e)
	}
	commerceRepo.WithLiveStreamAwards(f.repo)
	s := streamChatStart(t, f, "realtime-award")
	points, _ := live.ParseAwardPoints("10")
	o, e := f.repo.CreateAwardOrder(f.viewerCtx, f.third, s.ID, "realtime-award-one", live.AwardRequest{Amount: points})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = commerceRepo.PayOrder(f.viewerCtx, f.third, o.OrderID, &donor.ID); e != nil {
		t.Fatal(e)
	}
	var eventID, state string
	if e = f.db.QueryRow(`SELECT id::text,state FROM owned_livestream_chat_outbox WHERE stream_id=$1::uuid AND kind='award'`, s.ID).Scan(&eventID, &state); e != nil || state != "pending" {
		t.Fatal(eventID, state, e)
	}
	second, e := f.repo.CreateAwardOrder(f.viewerCtx, f.third, s.ID, "realtime-award-cap", live.AwardRequest{Amount: points})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = commerceRepo.PayOrder(f.viewerCtx, f.third, second.OrderID, &donor.ID); e == nil {
		t.Fatal("full realtime event capacity accepted a second paid award")
	}
	walletAssertBalance(t, wallets, donor.ID, currency, "90.000", "0.000", "90.000")
	walletAssertBalance(t, wallets, treasury.ID, currency, "1.000", "9.000", "1.000")
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_award_receipts WHERE stream_id=$1::uuid`, s.ID); n != 1 {
		t.Fatal("capacity failure did not roll back monetary receipt", n)
	}
	chatProcess(t, f)
	packets := f.data.snapshot()
	if len(packets) != 1 || !json.Valid(packets[0].Payload) || !strings.Contains(string(packets[0].Payload), `"type":"livestream.award"`) {
		t.Fatal(packets)
	}
	if state = chatEventState(t, f, eventID); state != "accepted" {
		t.Fatal(state)
	}
}
