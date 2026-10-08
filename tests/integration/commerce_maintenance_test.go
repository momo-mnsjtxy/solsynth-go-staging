// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"testing"
	"time"
)

func maintenanceSubscription(t *testing.T) (commerceFixture, c.Subscription) {
	t.Helper()
	f := newCommerceFixture(t)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1::uuid,false,$2::timestamptz)`, f.actor, f.config.Now())
	pin := walletpg.NewPINVerifier(f.db, nil).WithAuthority(f.guard).WithClock(f.config.Now)
	f.r.WithPaymentAuthorization(pin)
	s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "maintenance-quarantine", false)
	if e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, f.plan)
	if e != nil {
		t.Fatal(e)
	}
	ctx, e := pin.Atomic().PreparePayment(f.ctx, wallet.Principal{AccountID: f.actor, SessionID: f.principal.Session.ID}, "", wallet.PaymentOrder, wallet.OrderPaymentBinding(o.ID, &f.wallet.ID))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	s, e = f.r.Subscription(f.ctx, f.actor, f.plan)
	if e != nil {
		t.Fatal(e)
	}
	return f, s
}
func dueMaintenanceSubscription(t *testing.T, f commerceFixture, s c.Subscription, ended time.Time) c.Subscription {
	t.Helper()
	s.BegunAt = ended.Add(-24 * time.Hour)
	s.EndedAt = ended
	s.RenewalAt = &ended
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2::timestamptz,ended_at=$3::timestamptz,renewal_at=$3::timestamptz,document=$4::jsonb WHERE id=$1::uuid`, s.ID, s.BegunAt, s.EndedAt, b)
	return s
}
func TestPGCommerceMaintenanceQuarantineAllowsOtherRenewals(t *testing.T) {
	bad, s := maintenanceSubscription(t)
	good, valid := maintenanceSubscription(t)
	due := good.config.Now().Add(-time.Second)
	valid = dueMaintenanceSubscription(t, good, valid, due)
	query(t, bad.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2::timestamptz WHERE id=$1::uuid`, s.ID, due.Add(-time.Hour))
	var before []byte
	if e := bad.db.QueryRow(`SELECT document FROM commerce_v1.subscriptions WHERE id=$1::uuid`, s.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e := good.r.ProcessDue(good.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(good.db), good.wallet.ID, "synthetic-commerce", "93.334", "0.000", "93.334")
	walletAssertBalance(t, walletpg.New(bad.db), bad.wallet.ID, "synthetic-commerce", "96.667", "0.000", "96.667")
	if n := commerceCount(t, bad.db, `SELECT count(*)FROM commerce_v1.subscription_maintenance_quarantine WHERE subscription_id=$1::uuid AND resolved_at IS NULL AND reason='projection_mismatch' AND observations=1`, s.ID); n != 1 {
		t.Fatal("missing quarantine", n)
	}
	var after []byte
	if e := bad.db.QueryRow(`SELECT document FROM commerce_v1.subscriptions WHERE id=$1::uuid`, s.ID).Scan(&after); e != nil || string(before) != string(after) {
		t.Fatal("quarantine silently repaired financial data", e)
	}
	if _, e := good.r.ProcessDue(good.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, good.db, `SELECT count(*)FROM commerce_v1.outbox WHERE aggregate_id=$1::uuid AND event_type='commerce.subscription.renewed'`, valid.ID); n != 1 {
		t.Fatal("renewal duplicated", n)
	}
	failures, e := good.r.SubscriptionMaintenanceFailures(good.ctx, "", 1000)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range failures {
		if v.SubscriptionID == s.ID {
			found = true
			if len(v.DocumentSHA256) != 64 || v.Reason != "projection_mismatch" || v.Observations != 1 {
				t.Fatal(v)
			}
		}
	}
	if !found {
		t.Fatal("quarantine not inspectable")
	}
	if e = bad.r.RetrySubscriptionMaintenance(bad.ctx, s.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("invalid row released", e)
	}
	// Explicit operator repair of only this synthetic record, followed by retry.
	query(t, bad.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2::timestamptz WHERE id=$1::uuid`, s.ID, s.EndedAt)
	if e = bad.r.RetrySubscriptionMaintenance(bad.ctx, s.ID); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, bad.db, `SELECT count(*)FROM commerce_v1.subscription_maintenance_quarantine WHERE subscription_id=$1::uuid AND resolved_at IS NOT NULL`, s.ID); n != 1 {
		t.Fatal("resolved evidence erased", n)
	}
	walletAssertBalance(t, walletpg.New(bad.db), bad.wallet.ID, "synthetic-commerce", "96.667", "0.000", "96.667")
}
func TestPGCommerceMaintenanceMalformedIdentityAndBudget(t *testing.T) {
	_ = database(t)
	for _, kind := range []string{"malformed", "account", "payer"} {
		t.Run(kind, func(t *testing.T) {
			f, s := maintenanceSubscription(t)
			if n, e := f.r.ProcessDue(f.ctx, 1000); e != nil || n == 1000 {
				t.Fatal("synthetic precondition backlog", n, e)
			}
			past := time.Date(1901, 1, 1, 0, 0, 0, 0, time.UTC)
			s = dueMaintenanceSubscription(t, f, s, past)
			switch kind {
			case "malformed":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET document='[]'::jsonb WHERE id=$1::uuid`, s.ID)
			case "account":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(document,'{account_id}',to_jsonb($2::text))WHERE id=$1::uuid`, s.ID, authorityUUID(t))
			case "payer":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(document,'{payment_details,payer_wallet_id}',to_jsonb($2::text))WHERE id=$1::uuid`, s.ID, f.payee.ID)
			}
			count, e := f.r.ProcessDue(f.ctx, 1)
			if e != nil || count != 1 {
				t.Fatal("bounded quarantine decision", count, e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*)FROM commerce_v1.subscription_maintenance_quarantine WHERE subscription_id=$1::uuid AND resolved_at IS NULL`, s.ID); n != 1 {
				t.Fatal(n)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.667", "0.000", "96.667")
		})
	}
}
func TestPGCommerceMaintenanceQuarantineRollback(t *testing.T) {
	f, s := maintenanceSubscription(t)
	if n, e := f.r.ProcessDue(f.ctx, 1000); e != nil || n == 1000 {
		t.Fatal("synthetic precondition backlog", n, e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at='1900-01-01'::timestamptz WHERE id=$1::uuid`, s.ID)
	name := "maintenance_rollback_" + strings.ReplaceAll(s.ID, "-", "")
	query(t, f.db, `ALTER TABLE commerce_v1.subscription_maintenance_quarantine ADD CONSTRAINT `+name+` CHECK(subscription_id<>'`+s.ID+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE commerce_v1.subscription_maintenance_quarantine DROP CONSTRAINT IF EXISTS ` + name)
	})
	if _, e := f.r.ProcessDue(f.ctx, 1); e == nil {
		t.Fatal("injected quarantine failure committed")
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM commerce_v1.subscription_maintenance_quarantine WHERE subscription_id=$1::uuid`, s.ID); n != 0 {
		t.Fatal(n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.667", "0.000", "96.667")
	query(t, f.db, `ALTER TABLE commerce_v1.subscription_maintenance_quarantine DROP CONSTRAINT `+name)
	done := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; _, e := f.r.ProcessDue(f.ctx, 1000); done <- e }()
	}
	close(start)
	for range 2 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM commerce_v1.subscription_maintenance_quarantine WHERE subscription_id=$1::uuid AND observations=1 AND resolved_at IS NULL`, s.ID); n != 1 {
		t.Fatal("concurrent quarantine duplicated observation", n)
	}
}
func TestPGCommerceMaintenanceStaleCandidateContinues(t *testing.T) {
	first, s := maintenanceSubscription(t)
	second, next := maintenanceSubscription(t)
	s = dueMaintenanceSubscription(t, first, s, time.Date(1902, 1, 1, 0, 0, 0, 0, time.UTC))
	next = dueMaintenanceSubscription(t, second, next, second.config.Now().Add(-time.Second))
	tx, pid := atomicBlocker(t, first.atomicAuthorityFixture, "owned_authority_accounts", first.actor)
	done := make(chan error, 1)
	go func() { _, e := second.r.ProcessDue(second.ctx, 1000); done <- e }()
	waitAtomicBlocked(t, first.atomicAuthorityFixture, pid, "owned_authority_accounts")
	// A legitimate concurrent cancellation updates both canonical representations.
	s.Status = c.SubscriptionCancelled
	s.IsActive = false
	s.RenewalAt = nil
	b, _ := json.Marshal(s)
	if _, e := tx.Exec(`UPDATE commerce_v1.subscriptions SET status=$2::integer,renewal_at=NULL,document=$3::jsonb WHERE id=$1::uuid`, s.ID, s.Status, b); e != nil {
		t.Fatal(e)
	}
	if e := tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, second.db, `SELECT count(*)FROM commerce_v1.outbox WHERE aggregate_id=$1::uuid AND event_type='commerce.subscription.renewed'`, next.ID); n != 1 {
		t.Fatal("stale candidate stopped unrelated renewal", n)
	}
	if n := commerceCount(t, first.db, `SELECT count(*)FROM commerce_v1.subscription_maintenance_quarantine WHERE subscription_id=$1::uuid`, s.ID); n != 0 {
		t.Fatal("coherent cancellation quarantined", n)
	}
}
func TestPGCommerceMaintenanceQuarantineRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	file := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "commerce-maintenance-quarantine.json")
	if phase == "before" {
		f, s := maintenanceSubscription(t)
		query(t, db, `UPDATE commerce_v1.subscriptions SET ended_at='1903-01-01'::timestamptz WHERE id=$1::uuid`, s.ID)
		if _, e := f.r.ProcessDue(f.ctx, 1000); e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(s.ID)
		if e := os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if phase != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var id string
	if e = json.Unmarshal(b, &id); e != nil {
		t.Fatal(e)
	}
	r := commercepg.New(db, nil, commercepg.Adapters{})
	failures, e := r.SubscriptionMaintenanceFailures(context.Background(), "", 1000)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range failures {
		if v.SubscriptionID == id && v.Observations == 1 && v.Reason == "projection_mismatch" {
			return
		}
	}
	t.Fatal("quarantine missing after restart")
}
