// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
)

// This fixture receives an already installed, sealed-template clone. Only the
// before process may create accounts, keys, sessions, devices or pending grants.
func newIdentitySavedFixture(t *testing.T, db *sql.DB) (presenceProgressionFixture, authority.Config) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := presenceProgressionFixture{db: db, ctx: ctx, actor: authorityUUID(t)}
	if e := authoritypg.SeedSynthetic(ctx, db, f.actor, "synthetic_identity_saved_"+strings.ReplaceAll(f.actor, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	query(t, db, `INSERT INTO owned_accounts_profiles(account_id,id,time_zone,updated_at)VALUES($1,$2,'UTC',clock_timestamp())`, f.actor, authorityUUID(t))
	store := authoritypg.New(db)
	if e := store.ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: notifications.DevicePermission}, {Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	clock := &atomic.Int64{}
	clock.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), clock)
	initial, wall := cfg.Now().UTC(), time.Now()
	cfg.AccessTTL = time.Hour
	cfg.SessionTTL = time.Hour
	cfg.Now = func() time.Time { return initial.Add(time.Since(wall)) }
	var e error
	f.issuer, e = authority.New(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.verifier, e = authority.NewVerifier(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.principal, e = f.verifier.Authenticate(ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(store, f.verifier)
	f.ctx = f.guard.Bind(ctx, f.principal)
	f.progression = progressionpg.NewWithAuthority(db, f.guard)
	return f, cfg
}

// Exact row snapshots make replacement, after-phase repair or lost provenance
// observable before any worker runs. Markers contain only synthetic DB state
// and public verifier keys; the after process has no signer or bearer token.
func identitySavedRows(t *testing.T, db *sql.DB, actor, treasury string) map[string]string {
	t.Helper()
	statements := map[string]string{
		"authority":  `SELECT jsonb_build_object('accounts',(SELECT jsonb_agg(to_jsonb(a)ORDER BY id)FROM owned_authority_accounts a WHERE id IN($1::uuid,$2::uuid)),'sessions',(SELECT jsonb_agg(to_jsonb(s)ORDER BY id)FROM owned_authority_sessions s WHERE account_id IN($1::uuid,$2::uuid)),'policy',(SELECT jsonb_agg(to_jsonb(g)ORDER BY id)FROM owned_authority_grants g WHERE account_id IN($1::uuid,$2::uuid)))::text`,
		"grants":     `SELECT COALESCE(jsonb_agg(to_jsonb(g)ORDER BY id),'[]'::jsonb)::text FROM owned_progression_grants g WHERE account_id IN($1::uuid,$2::uuid)`,
		"wallets":    `SELECT jsonb_build_object('wallets',(SELECT jsonb_agg(to_jsonb(w)ORDER BY id)FROM wallet_v1.wallets w WHERE account_id IN($1::uuid,$2::uuid)),'pockets',(SELECT jsonb_agg(to_jsonb(p)ORDER BY p.id)FROM wallet_v1.pockets p JOIN wallet_v1.wallets w ON w.id=p.wallet_id WHERE w.account_id IN($1::uuid,$2::uuid)))::text`,
		"ledger":     `SELECT COALESCE(jsonb_agg(to_jsonb(x)ORDER BY id),'[]'::jsonb)::text FROM wallet_v1.transactions x WHERE actor_id IN($1::uuid,$2::uuid)`,
		"outbox":     `SELECT COALESCE(jsonb_agg(to_jsonb(x)ORDER BY id),'[]'::jsonb)::text FROM owned_progression_outbox x WHERE account_id IN($1::uuid,$2::uuid)`,
		"devices":    `SELECT COALESCE(jsonb_agg(to_jsonb(d)ORDER BY id),'[]'::jsonb)::text FROM notification_v1.devices d WHERE account_id IN($1::uuid,$2::uuid)`,
		"manifest":   `SELECT COALESCE(jsonb_agg(to_jsonb(b)ORDER BY delivery_id),'[]'::jsonb)::text FROM notification_v1.device_delivery_batches b WHERE account_id IN($1::uuid,$2::uuid)`,
		"receipts":   `SELECT COALESCE(jsonb_agg(to_jsonb(r)ORDER BY r.delivery_id,r.device_id),'[]'::jsonb)::text FROM notification_v1.device_delivery_receipts r JOIN notification_v1.device_delivery_batches b ON b.delivery_id=r.delivery_id WHERE b.account_id IN($1::uuid,$2::uuid)`,
		"deliveries": `SELECT COALESCE(jsonb_agg(to_jsonb(d)ORDER BY d.notification_id),'[]'::jsonb)::text FROM notification_v1.deliveries d JOIN notification_v1.inbox n ON n.id=d.notification_id WHERE n.account_id IN($1::uuid,$2::uuid)`,
	}
	out := map[string]string{}
	for key, q := range statements {
		var raw string
		if e := db.QueryRow(q, actor, treasury).Scan(&raw); e != nil {
			t.Fatal(key, e)
		}
		out[key] = raw
	}
	return out
}

func TestPGIdentityDeliverySavedRestartPersistence(t *testing.T) {
	type saved struct {
		Database                      isolatedRestartDatabase
		Authority                     restartAuthoritySnapshot
		Actor, Treasury, Payer, Payee string
		Config                        progressionpg.SourcePointConfig
		Grants                        map[string]int64
		Accepted, Uncertain           notifications.Delivery
		Rows                          map[string]string
	}
	const marker = "identity-delivery-saved.json"
	if fixtureRestartPhase(t) == "before" {
		db, identity := createIsolatedRestartDatabase(t, "identity")
		f, cfg := newIdentitySavedFixture(t, db)
		treasury := ppOther(t, f)
		payer, payee := pgSourceWallet(t, f, treasury, "100"), pgSourceWallet(t, f, f.actor, "0")
		source := pgSourceGrant(t, f, 25)
		provider := &pgPushDeviceProvider{}
		policy := notificationpg.New(db, f.guard, "synthetic")
		device, e := notificationpg.NewDevices(db, f.guard, notificationpg.DeviceConfig{EncryptionKey: make([]byte, 32), Provider: provider}, f.progression)
		if e != nil {
			t.Fatal(e)
		}
		device.WithDeliveryPolicy(policy)
		tokens := []string{}
		for range 2 {
			token := "synthetic-saved-device-" + authorityUUID(t)
			if _, e = device.StartDevice(f.ctx, f.actor, notifications.DeviceRequest{DeviceID: authorityUUID(t), Token: token, ConsentHours: 1}); e != nil {
				t.Fatal(e)
			}
			if _, e = device.ConfirmDevice(f.ctx, f.actor, provider.proof()); e != nil {
				t.Fatal(e)
			}
			tokens = append(tokens, token)
		}
		uncertain := pgDeviceDelivery(t, f, policy)
		provider.wake = func(_ context.Context, token string) error {
			if token == tokens[1] {
				return errors.New("synthetic ambiguous acceptance")
			}
			return nil
		}
		if ok, e := policy.DispatchOne(f.ctx, device); !ok || !errors.Is(e, notifications.ErrDeliveryUncertain) {
			t.Fatal(ok, e)
		}
		provider.wake = nil
		accepted := pgDeviceDelivery(t, f, policy)
		// Commit provider/device acknowledgments but leave the outer delivery due:
		// this models a process stopping between the two durable acknowledgments.
		if e = device.Send(f.ctx, accepted); e != nil {
			t.Fatal(e)
		}
		if pgWakeCount(provider) != 4 {
			t.Fatal("before saved manifest provider count", pgWakeCount(provider))
		}
		grants := map[string]int64{}
		rows, e := db.Query(`SELECT id::text,(reward->>'source_points')::bigint FROM owned_progression_grants WHERE account_id=$1 AND(reward->>'source_points')::bigint>0 ORDER BY id`, f.actor)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id string
			var points int64
			if e = rows.Scan(&id, &points); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			grants[id] = points
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
		// The real confirmed device producer has also earned the catalog's 3-point
		// first-push achievement, alongside the original synthetic 25-point grant.
		var sum int64
		for _, v := range grants {
			sum += v
		}
		if len(grants) != 2 || grants[source] != 25 || sum != 28 {
			t.Fatal("unexpected original pending sources", grants)
		}
		state := saved{Database: identity, Authority: saveAuthoritySnapshot(t, cfg), Actor: f.actor, Treasury: treasury, Payer: payer, Payee: payee, Config: progressionpg.SourcePointConfig{TreasuryAccountID: treasury, TreasuryWalletID: payer, Currency: "synthetic-earned-points", MaxPerGrant: 25, MaxPerDay: 28}, Grants: grants, Accepted: accepted, Uncertain: uncertain}
		assertIdentitySavedEffects(t, db, state.Actor, state.Treasury, state.Payer, state.Payee, state.Grants, false)
		assertIdentitySavedReceipt(t, db, accepted.Key, 2, 0, false)
		assertIdentitySavedReceipt(t, db, uncertain.Key, 1, 1, true)
		state.Rows = identitySavedRows(t, db, state.Actor, state.Treasury)
		saveRestartFixture(t, marker, state)
		return
	}
	var state saved
	loadRestartFixture(t, marker, &state)
	db := openIsolatedRestartDatabase(t, state.Database)
	if got := identitySavedRows(t, db, state.Actor, state.Treasury); !reflect.DeepEqual(got, state.Rows) {
		t.Fatal("original source/ledger/budget/outbox/device/proof rows changed across real restart")
	}
	verifier, e := authority.NewVerifier(authoritypg.New(db), restoreAuthoritySnapshot(t, state.Authority))
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payout, e := progressionpg.NewFundedSourcePoints(state.Config)
	if e != nil {
		t.Fatal(e)
	}
	newWorker := func() *progressionpg.Repository {
		return progressionpg.NewWithAuthority(db, guard).WithSourcePointPayout(payout)
	}
	if e = newWorker().Ready(ctx); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var count atomic.Int64
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, e := newWorker().ProcessSourcePoints(ctx, 100)
			count.Add(int64(n))
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
	if count.Load() != int64(len(state.Grants)) {
		t.Fatal("original pending sources not consumed exactly once", count.Load())
	}
	assertIdentitySavedEffects(t, db, state.Actor, state.Treasury, state.Payer, state.Payee, state.Grants, true)
	if n, e := newWorker().ProcessSourcePoints(ctx, 100); e != nil || n != 0 {
		t.Fatal("fresh worker replayed saved sources", n, e)
	}
	assertIdentitySavedEffects(t, db, state.Actor, state.Treasury, state.Payer, state.Payee, state.Grants, true)
	provider := &pgPushDeviceProvider{}
	policy := notificationpg.New(db, guard, "synthetic")
	device, e := notificationpg.NewDevices(db, guard, notificationpg.DeviceConfig{EncryptionKey: make([]byte, 32), Provider: provider}, newWorker())
	if e != nil {
		t.Fatal(e)
	}
	device.WithDeliveryPolicy(policy)
	if ok, e := policy.DispatchOne(ctx, device); !ok || e != nil {
		t.Fatal("accepted saved receipt failed outer acknowledgment", ok, e)
	}
	if ok, e := notificationpg.New(db, guard, "synthetic").DispatchOne(ctx, device); ok || e != nil {
		t.Fatal("terminal saved uncertain delivery became due", ok, e)
	}
	if pgWakeCount(provider) != 0 {
		t.Fatal("actual restart resent accepted or ambiguous device receipts", pgWakeCount(provider))
	}
	assertIdentitySavedReceipt(t, db, state.Accepted.Key, 2, 0, false)
	assertIdentitySavedReceipt(t, db, state.Uncertain.Key, 1, 1, true)
	var delivered bool
	if e = db.QueryRow(`SELECT delivered_at IS NOT NULL AND suppressed_at IS NULL AND lease_id IS NULL FROM notification_v1.deliveries WHERE notification_id=$1`, state.Accepted.Key).Scan(&delivered); e != nil || !delivered {
		t.Fatal("saved accepted receipt not durably acknowledged", e)
	}
	after := identitySavedRows(t, db, state.Actor, state.Treasury)
	for _, key := range []string{"authority", "devices", "manifest", "receipts"} {
		if after[key] != state.Rows[key] {
			t.Fatal("restart replaced original proof or device attempt", key)
		}
	}
}

func assertIdentitySavedEffects(t *testing.T, db *sql.DB, actor, treasury, payer, payee string, grants map[string]int64, paid bool) {
	t.Helper()
	want := 0
	if paid {
		want = 1
	}
	var sum int64
	for id, points := range grants {
		var marker, outbox, ledger int
		if e := db.QueryRow(`SELECT (SELECT count(*)FROM owned_progression_grants WHERE id=$1 AND account_id=$2 AND(reward->>'source_points')::bigint=$3 AND source_points_granted_at IS NOT NULL),(SELECT count(*)FROM owned_progression_outbox WHERE grant_id=$1 AND account_id=$2 AND packet_type='progression.points.granted'AND payload->>'grant_id'=$1::text AND(payload->>'source_points')::bigint=$3 AND payload->>'currency'='synthetic-earned-points'),(SELECT count(*)FROM wallet_v1.transactions WHERE actor_id=$4 AND idempotency_key='progression:'||$1::text AND amount=$3 AND status=2 AND payer_wallet_id=$5 AND payee_wallet_id=$6 AND currency='synthetic-earned-points'AND deleted_at IS NULL)`, id, actor, points, treasury, payer, payee).Scan(&marker, &outbox, &ledger); e != nil || marker != want || outbox != want || ledger != want {
			t.Fatal("saved actual worker effects", id, marker, outbox, ledger, e)
		}
		sum += points
	}
	var budget string
	if e := db.QueryRow(`SELECT COALESCE(sum(amount),0)::text FROM wallet_v1.transactions WHERE actor_id=$1 AND payer_wallet_id=$2 AND idempotency_key LIKE 'progression:%'AND status=2 AND currency='synthetic-earned-points'AND deleted_at IS NULL`, treasury, payer).Scan(&budget); e != nil {
		t.Fatal(e)
	}
	balance := jsonInt(sum) + ".000"
	wantBudget, wantPayee, wantPayer := "0.000", "0.000", "100.000"
	if paid {
		wantBudget, wantPayee = balance, balance
		wantPayer = jsonInt(100-sum) + ".000"
	}
	if budget != wantBudget || pgPointBalance(t, db, payer).String() != wantPayer || pgPointBalance(t, db, payee).String() != wantPayee {
		t.Fatal("saved payout budget/conservation differs", budget, wantBudget)
	}
}

func assertIdentitySavedReceipt(t *testing.T, db *sql.DB, id string, accepted, uncertain int, terminal bool) {
	t.Helper()
	var a, u, manifest int
	if e := db.QueryRow(`SELECT (SELECT count(*)FROM notification_v1.device_delivery_batches WHERE delivery_id=$1),(SELECT count(*)FROM notification_v1.device_delivery_receipts WHERE delivery_id=$1 AND state='accepted'AND accepted_at IS NOT NULL),(SELECT count(*)FROM notification_v1.device_delivery_receipts WHERE delivery_id=$1 AND state='uncertain'AND accepted_at IS NULL)`, id).Scan(&manifest, &a, &u); e != nil || manifest != 1 || a != accepted || u != uncertain {
		t.Fatal("saved per-device receipt state", manifest, a, u, e)
	}
	if terminal {
		var done bool
		if e := db.QueryRow(`SELECT suppressed_at IS NOT NULL AND delivered_at IS NULL AND lease_id IS NULL AND lease_until IS NULL AND last_error='DELIVERY_UNCERTAIN'FROM notification_v1.deliveries WHERE notification_id=$1`, id).Scan(&done); e != nil || !done {
			t.Fatal("saved uncertain outer result not terminal non-delivered", e)
		}
	}
}
