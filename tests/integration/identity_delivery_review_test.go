// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/connectionpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/progressionpg"
	a "solsynth.local/go-migration/ports/admin/accounts"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/notifications"
	links "solsynth.local/go-migration/ports/passport/connections"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPGProviderChallengeCapsShortSessionExpiry(t *testing.T) {
	f := newPresenceProgressionFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: links.Permission}}, nil); e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	cfg.SessionTTL = 2 * time.Minute
	cfg.AccessTTL = time.Minute
	issuer, e := authority.New(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	p, e := verifier.Authenticate(f.ctx, pgIssue(t, issuer, f.actor).Token)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	ctx := g.Bind(f.ctx, p)
	r, e := connectionpg.New(f.db, g, connectionpg.Config{EncryptionKey: make([]byte, 32), Providers: []links.Provider{pgVerifiedProvider{identifier: "76561198000000008", calls: &atomic.Int64{}}}})
	if e != nil {
		t.Fatal(e)
	}
	start, e := r.Start(ctx, f.actor, "steam")
	if e != nil {
		t.Fatal("short valid session could not start provider verification", e)
	}
	hash := sha256.Sum256([]byte(start.State))
	var created, expires time.Time
	if e = f.db.QueryRow(`SELECT created_at,expires_at FROM owned_account_connection_challenges WHERE state_hash=$1`, hash[:]).Scan(&created, &expires); e != nil {
		t.Fatal(e)
	}
	if !expires.Equal(p.Session.ExpiresAt) || !expires.After(created) || expires.Sub(created) >= 10*time.Minute {
		t.Fatal("challenge not bounded by originating short session", created, expires, p.Session.ExpiresAt)
	}
	if _, e = r.Complete(ctx, f.actor, "steam", links.Completion{State: start.State, Code: "synthetic"}); e != nil {
		t.Fatal(e)
	}
}

func TestPGAccountAdminCannotReplaceOtherRootSecurityIdentity(t *testing.T) {
	f := newAccountAdminFixture(t)
	email := "synthetic-other-root-" + f.other + "@example.invalid"
	out, e := f.repo.MutateAccount(f.ctx, f.actor, f.other, a.Mutation{Operation: "contact.create", Contact: &signin.ContactInput{Type: 0, Content: email}})
	if e != nil {
		t.Fatal(e)
	}
	contact := out.(signin.Contact)
	factor := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_authority_factors(id,account_id,type,created_at,enabled_at,secret)VALUES($1,$2,3,clock_timestamp(),clock_timestamp(),'synthetic'::bytea)`, factor, f.other)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.other)
	var session string
	if e = f.db.QueryRow(`SELECT id::text FROM owned_authority_sessions WHERE account_id=$1 ORDER BY id LIMIT 1`, f.other).Scan(&session); e != nil {
		t.Fatal(e)
	}
	password := "synthetic-root-takeover-password"
	before := accountAdminIdentityState(t, f.db, f.actor, f.other)
	for _, m := range []a.Mutation{
		{Operation: "password.reset", Password: &password},
		{Operation: "contact.create", Contact: &signin.ContactInput{Type: 0, Content: "synthetic-replacement@example.invalid"}},
		{Operation: "contact.verify", ResourceID: contact.ID},
		{Operation: "contact.primary", ResourceID: contact.ID},
		{Operation: "contact.delete", ResourceID: contact.ID},
		{Operation: "factor.delete", ResourceID: factor},
		{Operation: "session.revoke", ResourceID: session},
	} {
		if _, e = f.repo.MutateAccount(f.ctx, f.actor, f.other, m); !errors.Is(e, c.ErrForbidden) {
			t.Fatal("other root security mutation admitted", m.Operation, e)
		}
	}
	if after := accountAdminIdentityState(t, f.db, f.actor, f.other); after != before {
		t.Fatal("root identity changed despite forbidden mutations")
	}
	var unchanged bool
	if e = f.db.QueryRow(`SELECT verified_at IS NULL AND deleted_at IS NULL AND NOT is_public FROM owned_authority_contacts WHERE id=$1`, contact.ID).Scan(&unchanged); e != nil || !unchanged {
		t.Fatal("other root contact changed", e)
	}
}

func pgDeviceDelivery(t *testing.T, f presenceProgressionFixture, policy *notificationpg.Repository) notifications.Delivery {
	t.Helper()
	id, e := policy.Enqueue(f.ctx, notifications.Command{AccountID: f.actor, AppID: "synthetic", Key: "synthetic-device-delivery:" + authorityUUID(t), Topic: "synthetic.push", Title: "Synthetic wakeup", Content: "Private text must never be pushed", External: true})
	if e != nil {
		t.Fatal(e)
	}
	if n, e := policy.ProcessPending(f.ctx, 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	return notifications.Delivery{Key: id, Notification: notifications.Notification{ID: id, AccountID: f.actor, AppID: "synthetic"}}
}
func pgDeliveryDevices(t *testing.T, n int) (presenceProgressionFixture, *pgPushDeviceProvider, *notificationpg.DeviceStore, *notificationpg.Repository, []string) {
	t.Helper()
	db, _ := createIsolatedRestartDatabase(t, "devicereceipts")
	f, _ := newIdentitySavedFixture(t, db)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: notifications.DevicePermission}, {Pattern: "progression.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	p := &pgPushDeviceProvider{}
	cfg := notificationpg.DeviceConfig{EncryptionKey: make([]byte, 32), Provider: p}
	s, e := notificationpg.NewDevices(f.db, f.guard, cfg, f.progression)
	if e != nil {
		t.Fatal(e)
	}
	policy := notificationpg.New(f.db, f.guard, "synthetic")
	s.WithDeliveryPolicy(policy)
	tokens := []string{}
	for range n {
		token := "synthetic-durable-device-" + authorityUUID(t)
		id := authorityUUID(t)
		if _, e = s.StartDevice(f.ctx, f.actor, notifications.DeviceRequest{DeviceID: id, Token: token, ConsentHours: 1}); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ConfirmDevice(f.ctx, f.actor, p.proof()); e != nil {
			t.Fatal(e)
		}
		tokens = append(tokens, token)
	}
	return f, p, s, policy, tokens
}
func pgWakeCount(p *pgPushDeviceProvider) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, v := range p.wakeCalls {
		n += v
	}
	return n
}
func pgRestartDeviceStore(t *testing.T, f presenceProgressionFixture, p *pgPushDeviceProvider, policy *notificationpg.Repository) *notificationpg.DeviceStore {
	t.Helper()
	s, e := notificationpg.NewDevices(f.db, f.guard, notificationpg.DeviceConfig{EncryptionKey: make([]byte, 32), Provider: p}, progressionpg.NewWithAuthority(f.db, f.guard))
	if e != nil {
		t.Fatal(e)
	}
	return s.WithDeliveryPolicy(policy)
}

func TestPGNotificationDeviceDeliveryReceiptsPartialRestartAndUncertain(t *testing.T) {
	t.Run("partial result remains deduplicated", func(t *testing.T) {
		f, p, s, policy, tokens := pgDeliveryDevices(t, 2)
		v := pgDeviceDelivery(t, f, policy)
		p.wake = func(ctx context.Context, token string) error {
			if token == tokens[1] {
				return errors.New("synthetic unknown acceptance")
			}
			return nil
		}
		if ok, e := policy.DispatchOne(f.ctx, s); !ok || !errors.Is(e, notifications.ErrDeliveryUncertain) {
			t.Fatal(ok, e)
		}
		var accepted, uncertain int
		if e := f.db.QueryRow(`SELECT count(*)FILTER(WHERE state='accepted'),count(*)FILTER(WHERE state='uncertain')FROM notification_v1.device_delivery_receipts WHERE delivery_id=$1`, v.Key).Scan(&accepted, &uncertain); e != nil || accepted != 1 || uncertain != 1 {
			t.Fatal(accepted, uncertain, e)
		}
		p.wake = nil
		query(t, f.db, `UPDATE notification_v1.deliveries SET next_attempt_at=clock_timestamp()-interval '1 second'WHERE notification_id=$1`, v.Key)
		if ok, e := policy.DispatchOne(f.ctx, pgRestartDeviceStore(t, f, p, policy)); ok || e != nil {
			t.Fatal(ok, e)
		}
		if pgWakeCount(p) != 2 {
			t.Fatal("partial retry duplicated accepted device", pgWakeCount(p))
		}
		var delivered, suppressed *time.Time
		var code string
		if e := f.db.QueryRow(`SELECT delivered_at,suppressed_at,last_error FROM notification_v1.deliveries WHERE notification_id=$1`, v.Key).Scan(&delivered, &suppressed, &code); e != nil || delivered != nil || suppressed == nil || code != "DELIVERY_UNCERTAIN" {
			t.Fatal("uncertain result falsely acknowledged", code, e)
		}
	})
	t.Run("acceptance before acknowledgment crash", func(t *testing.T) {
		f, p, s, policy, _ := pgDeliveryDevices(t, 1)
		v := pgDeviceDelivery(t, f, policy)
		ctx, cancel := context.WithCancel(f.ctx)
		p.wake = func(context.Context, string) error { cancel(); return nil }
		if ok, e := policy.DispatchOne(ctx, s); !ok || !errors.Is(e, context.Canceled) {
			t.Fatal(ok, e)
		}
		query(t, f.db, `UPDATE notification_v1.device_delivery_receipts SET lease_until=clock_timestamp()-interval '1 second'WHERE delivery_id=$1 AND state='sending'`, v.Key)
		query(t, f.db, `UPDATE notification_v1.deliveries SET lease_until=clock_timestamp()-interval '1 second',next_attempt_at=clock_timestamp()-interval '1 second'WHERE notification_id=$1`, v.Key)
		p.wake = nil
		if ok, e := policy.DispatchOne(f.ctx, pgRestartDeviceStore(t, f, p, policy)); !ok || !errors.Is(e, notifications.ErrDeliveryUncertain) {
			t.Fatal(ok, e)
		}
		if pgWakeCount(p) != 1 {
			t.Fatal("restart blindly resent an uncertain acceptance", pgWakeCount(p))
		}
		if ok, e := policy.DispatchOne(f.ctx, pgRestartDeviceStore(t, f, p, policy)); ok || e != nil {
			t.Fatal("uncertain outer delivery never terminated", ok, e)
		}
		var terminal bool
		if e := f.db.QueryRow(`SELECT suppressed_at IS NOT NULL AND delivered_at IS NULL AND lease_id IS NULL AND last_error='DELIVERY_UNCERTAIN'FROM notification_v1.deliveries WHERE notification_id=$1`, v.Key).Scan(&terminal); e != nil || !terminal {
			t.Fatal("uncertain outer delivery falsely acknowledged or left due", e)
		}
	})
}

func TestPGNotificationDeviceFencedDispatchAndAuthorityDuringIO(t *testing.T) {
	t.Run("original admission revoked before I/O", func(t *testing.T) {
		f, p, s, policy, _ := pgDeliveryDevices(t, 1)
		v := pgDeviceDelivery(t, f, policy)
		query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp()WHERE id=$1`, f.principal.Session.ID)
		if e := s.Send(f.ctx, v); !errors.Is(e, notifications.ErrDeliveryUnconfigured) {
			t.Fatal("revoked original device admission sent", e)
		}
		if pgWakeCount(p) != 0 {
			t.Fatal("provider read before original device authority", pgWakeCount(p))
		}
	})
	t.Run("multiple instances", func(t *testing.T) {
		f, p, s, policy, _ := pgDeliveryDevices(t, 1)
		v := pgDeviceDelivery(t, f, policy)
		entered, release := make(chan struct{}), make(chan struct{})
		p.wake = func(ctx context.Context, _ string) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		done := make(chan error, 1)
		go func() { done <- s.Send(f.ctx, v) }()
		select {
		case <-entered:
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
		if e := pgRestartDeviceStore(t, f, p, policy).Send(f.ctx, v); !errors.Is(e, notifications.ErrDeliveryInProgress) {
			t.Fatal("concurrent dispatch crossed attempt fence", e)
		}
		close(release)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		if e := pgRestartDeviceStore(t, f, p, policy).Send(f.ctx, v); e != nil {
			t.Fatal(e)
		}
		if pgWakeCount(p) != 1 {
			t.Fatal("accepted delivery key duplicated", pgWakeCount(p))
		}
	})
	t.Run("no database locks across provider I/O", func(t *testing.T) {
		f, p, s, policy, _ := pgDeliveryDevices(t, 1)
		v := pgDeviceDelivery(t, f, policy)
		p.wake = func(ctx context.Context, _ string) error {
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_, e := f.db.ExecContext(bounded, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			return e
		}
		if e := s.Send(f.ctx, v); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("post-I/O authority loss not reconciled or canonical account lock held across I/O", e)
		}
		var accepted int
		if e := f.db.QueryRow(`SELECT count(*)FROM notification_v1.device_delivery_receipts WHERE delivery_id=$1 AND state='accepted'`, v.Key).Scan(&accepted); e != nil || accepted != 1 {
			t.Fatal("confirmed provider acceptance lost its dedupe receipt", accepted, e)
		}
	})
}

func TestPGProgressionSourcePointsActualWorkerAtomicRestart(t *testing.T) {
	db, _ := createIsolatedRestartDatabase(t, "pointsatomic")
	f, _ := newIdentitySavedFixture(t, db)
	treasury := ppOther(t, f)
	payer := pgSourceWallet(t, f, treasury, "0")
	payee := pgSourceWallet(t, f, f.actor, "0")
	blocked := f
	blocked.actor = ppOther(t, f)
	pgSourceGrant(t, blocked, 1)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, blocked.actor)
	grant := pgSourceGrant(t, f, 25)
	p, e := progressionpg.NewFundedSourcePoints(progressionpg.SourcePointConfig{TreasuryAccountID: treasury, TreasuryWalletID: payer, Currency: "synthetic-earned-points", MaxPerGrant: 100, MaxPerDay: 1000})
	if e != nil {
		t.Fatal(e)
	}
	newWorker := func() *progressionpg.Repository {
		return progressionpg.NewWithAuthority(f.db, f.guard).WithSourcePointPayout(p)
	}
	if n, e := newWorker().ProcessSourcePoints(f.ctx, 1); e == nil || n != 0 {
		t.Fatal("unfunded actual worker reported success", n, e)
	}
	assert := func(marked, outbox, transfers int) {
		t.Helper()
		var a, b, c int
		if e := f.db.QueryRow(`SELECT (SELECT count(*)FROM owned_progression_grants WHERE id=$1 AND source_points_granted_at IS NOT NULL),(SELECT count(*)FROM owned_progression_outbox WHERE grant_id=$1 AND packet_type='progression.points.granted'),(SELECT count(*)FROM wallet_v1.transactions WHERE idempotency_key=$2)`, grant, "progression:"+grant).Scan(&a, &b, &c); e != nil || a != marked || b != outbox || c != transfers {
			t.Fatal("actual worker atomic effects", a, b, c, e)
		}
	}
	assert(0, 0, 0)
	if pgPointBalance(t, f.db, payer).Sign() != 0 || pgPointBalance(t, f.db, payee).Sign() != 0 {
		t.Fatal("unfunded worker changed balance")
	}
	query(t, f.db, `UPDATE wallet_v1.pockets SET amount=100 WHERE wallet_id=$1 AND currency='synthetic-earned-points'`, payer)
	// Fail after the transfer has run, while publishing its committed payout.
	// Marker, outbox, transaction and both balances must still roll back.
	query(t, f.db, `CREATE FUNCTION owned_synthetic_point_outbox_failure()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.packet_type='progression.points.granted'THEN RAISE EXCEPTION 'synthetic payout outbox failure';END IF;RETURN NEW;END;$$;CREATE TRIGGER owned_synthetic_point_outbox_failure BEFORE INSERT ON owned_progression_outbox FOR EACH ROW EXECUTE FUNCTION owned_synthetic_point_outbox_failure()`)
	if n, e := newWorker().ProcessSourcePoints(f.ctx, 100); e == nil || n != 0 {
		t.Fatal("post-transfer worker failure reported success", n, e)
	}
	assert(0, 0, 0)
	if pgPointBalance(t, f.db, payer).String() != "100.000" || pgPointBalance(t, f.db, payee).Sign() != 0 {
		t.Fatal("post-transfer worker failure leaked balances")
	}
	query(t, f.db, `DROP TRIGGER owned_synthetic_point_outbox_failure ON owned_progression_outbox;DROP FUNCTION owned_synthetic_point_outbox_failure()`)
	var wg sync.WaitGroup
	var count atomic.Int64
	var errs sync.Mutex
	var failures []error
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, e := newWorker().ProcessSourcePoints(f.ctx, 100)
			count.Add(int64(n))
			if e != nil {
				errs.Lock()
				failures = append(failures, e)
				errs.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(failures) != 0 || count.Load() != 1 {
		t.Fatal("concurrent actual worker", count.Load(), failures)
	}
	assert(1, 1, 1)
	if pgPointBalance(t, f.db, payer).String() != "75.000" || pgPointBalance(t, f.db, payee).String() != "25.000" {
		t.Fatal("actual worker did not conserve treasury funds")
	}
	if n, e := newWorker().ProcessSourcePoints(f.ctx, 100); e != nil || n != 0 {
		t.Fatal("restart replayed paid grant", n, e)
	}
	assert(1, 1, 1)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, treasury)
	grant = pgSourceGrant(t, f, 10)
	if n, e := newWorker().ProcessSourcePoints(f.ctx, 100); e == nil || n != 0 {
		t.Fatal("disabled treasury actual worker reported success", n, e)
	}
	assert(0, 0, 0)
	if pgPointBalance(t, f.db, payer).String() != "75.000" || pgPointBalance(t, f.db, payee).String() != "25.000" {
		t.Fatal("disabled treasury worker leaked balances")
	}
}
