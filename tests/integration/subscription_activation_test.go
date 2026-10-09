// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync"
	"testing"
	"time"
)

type activationFixture struct {
	commerceFixture
	group         string
	subscriptions []c.Subscription
}

func newActivationFixture(t *testing.T, prefix string) activationFixture {
	t.Helper()
	f := activationFixture{commerceFixture: newCommerceFixture(t)}
	f.clock.Store(f.config.Now().Unix())
	f.group = prefix + "-" + f.app
	for i, days := range []int{1, 2, 3} {
		d := c.Definition{Identifier: fmt.Sprintf("%s-activation-%d", f.plan, i), GroupIdentifier: f.group, DisplayName: fmt.Sprintf("Synthetic queued plan %d", i), Currency: "synthetic-commerce", BasePrice: walletAmount(t, "3.333"), CycleDurationDays: days, AllowInternalWalletRenewal: true, AllowedPaymentMethods: []string{"solian.wallet"}, ProviderMappings: map[string][]string{}}
		raw, _ := json.Marshal(d)
		query(t, f.db, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id) VALUES($1,$2,$3)`, d.Identifier, raw, f.payee.ID)
		renew := i == 0
		s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier, IsAutoRenewal: &renew}, fmt.Sprintf("queue-%d", i), false)
		if e != nil {
			t.Fatal(e)
		}
		o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.Identifier)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
			t.Fatal(e)
		}
		s, e = f.r.Subscription(f.ctx, f.actor, s.Identifier)
		if e != nil {
			t.Fatal(e)
		}
		f.subscriptions = append(f.subscriptions, s)
	}
	return f
}
func activationDocuments(t *testing.T, f activationFixture) string {
	t.Helper()
	rows, e := f.db.Query(`SELECT document::text FROM commerce_v1.subscriptions WHERE account_id=$1 AND document->>'group_identifier'=$2 ORDER BY id`, f.actor, f.group)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	docs := []string{}
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			t.Fatal(e)
		}
		docs = append(docs, s)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return strings.Join(docs, "\n")
}

func TestPGSubscriptionActivationConservesPaidTimeAndMoney(t *testing.T) {
	f := newActivationFixture(t, "synthetic-activation")
	pending, e := f.r.PendingActivations(f.ctx, f.actor, c.PageQuery{Take: 1})
	if e != nil || pending.TotalCount != 2 || len(pending.Subscriptions) != 1 || pending.Subscriptions[0].ID != f.subscriptions[1].ID || pending.NextActivationAt == nil {
		t.Fatal(pending, e)
	}
	f.clock.Add(12 * 3600)
	now := time.Unix(f.clock.Load(), 0).UTC()
	end := f.subscriptions[2].EndedAt
	beforeLedger := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE actor_id=$1`, f.actor)
	g, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, f.subscriptions[1].ID)
	if e != nil || g.Current == nil || g.Current.Subscription.ID != f.subscriptions[1].ID || g.Next == nil || g.Next.Subscription.ID != f.subscriptions[0].ID {
		t.Fatal(g, e)
	}
	if !g.Current.Subscription.BegunAt.Equal(now) || g.Current.Subscription.RenewalAt != nil || g.Current.Subscription.IsAutoRenewal {
		t.Fatal("opt-out restored", g.Current)
	}
	if g.Next.Subscription.EndedAt.Sub(g.Next.Subscription.BegunAt) != 12*time.Hour || g.Next.Subscription.RenewalAt == nil || !g.Next.Subscription.RenewalAt.Equal(g.Next.Subscription.EndedAt) {
		t.Fatal("elapsed duration restored or renewal lost", g.Next)
	}
	if last := g.Subscriptions[len(g.Subscriptions)-1].Subscription; !last.EndedAt.Equal(end) {
		t.Fatal("paid time changed", last)
	}
	before := activationDocuments(t, f)
	if _, e = f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, f.subscriptions[1].ID); e != nil || before != activationDocuments(t, f) {
		t.Fatal("current-target replay changed state", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE event_type='commerce.subscription.activation_switched' AND aggregate_id=$1`, f.subscriptions[1].ID); n != 1 {
		t.Fatal("duplicate switch event", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE actor_id=$1`, f.actor); n != beforeLedger {
		t.Fatal("activation moved money", n, beforeLedger)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "90.001", "0.000", "90.001")
	page, e := f.r.PendingActivations(f.ctx, f.actor, c.PageQuery{Offset: 1, Take: 1})
	if e != nil || page.TotalCount != 2 || len(page.Subscriptions) != 1 || page.Subscriptions[0].ID != f.subscriptions[2].ID {
		t.Fatal(page, e)
	}
}

func TestPGSubscriptionActivationRollbackAndConcurrentReplay(t *testing.T) {
	f := newActivationFixture(t, "synthetic-activation-atomic")
	target := f.subscriptions[1].ID
	before := activationDocuments(t, f)
	query(t, f.db, `ALTER TABLE commerce_v1.outbox ADD CONSTRAINT synthetic_activation_failure CHECK(aggregate_id<>'`+target+`'::uuid OR event_type<>'commerce.subscription.activation_switched') NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE commerce_v1.outbox DROP CONSTRAINT IF EXISTS synthetic_activation_failure`)
	})
	if _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, target); e == nil {
		t.Fatal("outbox failure did not roll back")
	}
	if activationDocuments(t, f) != before {
		t.Fatal("failed activation partly rescheduled grants")
	}
	query(t, f.db, `ALTER TABLE commerce_v1.outbox DROP CONSTRAINT synthetic_activation_failure`)
	f.r.WithClock(time.Now)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		r, ctx := commercePool(t, f.commerceFixture)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.SwitchSubscriptionActivation(ctx, f.actor, f.group, target)
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
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE event_type='commerce.subscription.activation_switched' AND aggregate_id=$1`, target); n != 1 {
		t.Fatal("concurrent switch duplicated effects", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "90.001", "0.000", "90.001")
}

func TestPGSubscriptionActivationCurrentAuthorityAndIsolation(t *testing.T) {
	f := newActivationFixture(t, "synthetic-activation-authority")
	target := f.subscriptions[1].ID
	before := activationDocuments(t, f)
	if _, e := f.r.SwitchSubscriptionActivation(context.Background(), f.actor, f.group, target); e == nil {
		t.Fatal("missing authority accepted")
	}
	other := newCommerceFixture(t)
	if _, e := other.r.SwitchSubscriptionActivation(other.ctx, other.actor, f.group, target); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("foreign grant activated", e)
	}
	if _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, "different-group", target); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("cross-group target activated", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "subscriptions.cancel"}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, target); e == nil {
		t.Fatal("missing groups permission accepted")
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "subscriptions.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	block, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_authority_accounts", f.actor)
	done := make(chan error, 1)
	go func() { _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, target); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
	if _, e := block.Exec(`UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	if e := block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("disabled account changed activation after wait")
	}
	if activationDocuments(t, f) != before {
		t.Fatal("denied activation changed paid time")
	}
}

func TestPGSubscriptionActivationRejectsUnpaidCancelledAndExpired(t *testing.T) {
	f := newActivationFixture(t, "synthetic-activation-states")
	if _, e := f.r.CancelSubscription(f.ctx, f.actor, f.subscriptions[1].ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, f.subscriptions[1].ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("cancelled grant reactivated", e)
	}
	s := f.subscriptions[2]
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(document,'{is_active}','false') WHERE id=$1`, s.ID)
	if _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, s.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("disabled grant activated", e)
	}
	s.BegunAt = f.config.Now().Add(-48 * time.Hour)
	s.EndedAt = f.config.Now().Add(-24 * time.Hour)
	s.RenewalAt = nil
	raw, _ := json.Marshal(s)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2,ended_at=$3,renewal_at=NULL,document=$4 WHERE id=$1`, s.ID, s.BegunAt, s.EndedAt, raw)
	if _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, s.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("expired grant reactivated", e)
	}
	d := c.Definition{Identifier: f.plan + "-unpaid", GroupIdentifier: f.group, DisplayName: "Synthetic unpaid", Currency: "synthetic-commerce", BasePrice: walletAmount(t, "1"), CycleDurationDays: 1, AllowedPaymentMethods: []string{"solian.wallet"}}
	raw, _ = json.Marshal(d)
	query(t, f.db, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id) VALUES($1,$2,$3)`, d.Identifier, raw, f.payee.ID)
	u, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: d.Identifier}, "unpaid", false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, u.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("unpaid grant activated", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "90.001", "0.000", "90.001")
}

func TestPGSubscriptionActivationRevokedAppConsent(t *testing.T) {
	f := newRecurringProductFixture(t)
	group := "synthetic-activation"
	var raw []byte
	if e := f.db.QueryRow(`SELECT document FROM owned_app_products WHERE id=$1`, f.product).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var p apps.Product
	if e := json.Unmarshal(raw, &p); e != nil {
		t.Fatal(e)
	}
	p.GroupIdentifier = &group
	raw, _ = json.Marshal(p)
	query(t, f.db, `UPDATE owned_app_products SET document=$2 WHERE id=$1`, f.product, raw)
	recurringSync(t, f)
	p.ID = authorityUUID(t)
	p.Identifier = "synthetic-second"
	raw, _ = json.Marshal(p)
	query(t, f.db, `INSERT INTO owned_app_products(id,app_id,identifier,document,created_at,updated_at) VALUES($1,$2,$3,$4,now(),now())`, p.ID, p.AppID, p.Identifier, raw)
	copy := f
	copy.product = p.ID
	recurringSync(t, copy)
	var target string
	for _, id := range []string{f.product, p.ID} {
		s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: "app-product:" + id}, id, false)
		if e != nil {
			t.Fatal(e)
		}
		o, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.Identifier)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
			t.Fatal(e)
		}
		target = s.ID
	}
	if _, e := apppg.NewWithAuthority(f.db, f.guard).Consent(f.ctx, f.actor, "commerce-"+f.app, nil, true); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, "developer.app:"+f.app+":"+group, target); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revoked app consent activated paid grant", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.subscription.activation_switched'`, target); n != 0 {
		t.Fatal(n)
	}
}

func TestPGBackendSubscriptionActivationRoutes(t *testing.T) {
	f := newActivationFixture(t, "synthetic-activation-host")
	host := backendApp(t, f.db, f.config)
	path := "/wallet/subscriptions/groups/" + f.group + "/activate"
	backendJSONCall(t, host, "POST", path, "", map[string]any{"subscription_id": f.subscriptions[1].ID}, 401)
	backendCall(t, host, "POST", path, f.token, `{"subscription_id":"bad"}`, 400)
	pending := backendDecode[c.PendingActivations](t, backendCall(t, host, "GET", "/wallet/subscriptions/pending-activations?take=1", f.token, "", 200))
	if pending.TotalCount != 2 || len(pending.Subscriptions) != 1 {
		t.Fatal(pending)
	}
	g := backendDecode[c.GroupState](t, backendJSONCall(t, host, "POST", path, f.token, map[string]any{"subscription_id": f.subscriptions[1].ID}, 200))
	if g.Current == nil || g.Current.Subscription.ID != f.subscriptions[1].ID {
		t.Fatal(g)
	}
	backendJSONCall(t, host, "POST", path, f.token, map[string]any{"subscription_id": f.subscriptions[1].ID, "amount": 1}, 400)
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "90.001", "0.000", "90.001")
}

func TestPGSubscriptionActivationRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "subscription-activation-restart.json"
	type saved struct {
		Group, Target, Actor, Payer, Event string
		Subscriptions                      []c.Subscription
		TransactionCount                   int
	}
	if phase == "before" {
		f := newActivationFixture(t, "synthetic-activation-restart")
		g, e := f.r.SwitchSubscriptionActivation(f.ctx, f.actor, f.group, f.subscriptions[1].ID)
		if e != nil {
			t.Fatal(e)
		}
		state := saved{Group: f.group, Target: f.subscriptions[1].ID, Actor: f.actor, Payer: f.wallet.ID, TransactionCount: commerceCount(t, db, `SELECT count(*) FROM wallet_v1.transactions WHERE actor_id=$1`, f.actor)}
		for _, item := range g.Subscriptions {
			state.Subscriptions = append(state.Subscriptions, item.Subscription)
		}
		if e = db.QueryRow(`SELECT event_id::text FROM commerce_v1.outbox WHERE event_type='commerce.subscription.activation_switched' AND aggregate_id=$1`, state.Target).Scan(&state.Event); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, state)
		return
	}
	var state saved
	loadRestartFixture(t, marker, &state)
	var raw []byte
	var e error
	if !authority.ValidID(state.Target) || !authority.ValidID(state.Actor) || !authority.ValidID(state.Event) || len(state.Subscriptions) != 3 {
		t.Fatal("invalid saved restart identity", state)
	}
	seen := map[string]bool{}
	for _, want := range state.Subscriptions {
		if !authority.ValidID(want.ID) || seen[want.ID] || want.AccountID != state.Actor || !authority.ValidID(want.PaymentDetails.OrderID) {
			t.Fatal("invalid saved grant", want)
		}
		seen[want.ID] = true
		if e = db.QueryRow(`SELECT document FROM commerce_v1.subscriptions WHERE id=$1 AND account_id=$2`, want.ID, state.Actor).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var got c.Subscription
		if e = json.Unmarshal(raw, &got); e != nil {
			t.Fatal(e)
		}
		actual, _ := json.Marshal(got)
		expected, _ := json.Marshal(want)
		if string(actual) != string(expected) {
			t.Fatal("persisted grant differs from exact saved state", got, want)
		}
		if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.subscriptions WHERE id=$1 AND account_id=$2 AND identifier=$3 AND status=$4 AND begun_at=$5 AND ended_at=$6 AND renewal_at IS NOT DISTINCT FROM $7::timestamptz AND cycle_days=$8 AND auto_renew=$9`, want.ID, state.Actor, want.Identifier, want.Status, want.BegunAt, want.EndedAt, want.RenewalAt, want.CycleDurationDays, want.IsAutoRenewal); n != 1 {
			t.Fatal("persisted scalar projection differs from saved state", want.ID)
		}
	}
	if !seen[state.Target] {
		t.Fatal("target missing from saved grants")
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.outbox WHERE event_id=$1 AND aggregate_id=$2 AND event_type='commerce.subscription.activation_switched' AND payload->>'group_identifier'=$3`, state.Event, state.Target, state.Group); n != 1 {
		t.Fatal("saved switch event missing", n)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM wallet_v1.transactions WHERE actor_id=$1`, state.Actor); n != state.TransactionCount || n != 3 {
		t.Fatal("restart changed charge count", n)
	}
	walletAssertBalance(t, walletpg.New(db), state.Payer, "synthetic-commerce", "90.001", "0.000", "90.001")
}
