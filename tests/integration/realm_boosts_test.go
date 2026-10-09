// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/realmboostpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	"solsynth.local/go-migration/ports/passport/social"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type realmBoostFixture struct {
	commerceFixture
	boosts   *realmboostpg.Repository
	social   *socialpg.Repository
	realm    social.Realm
	cfg      realmboostpg.Config
	boostNow *atomic.Int64
}

func newRealmBoostFixture(t *testing.T) realmBoostFixture {
	t.Helper()
	f := realmBoostFixture{commerceFixture: newCommerceFixture(t), boostNow: &atomic.Int64{}}
	query(t, f.db, realmboostpg.Schema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "wallets.*"}, {Pattern: "realms.*"}, {Pattern: "subscriptions.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.boostNow.Store(f.clock.Load() * int64(time.Second))
	now := func() time.Time { return time.Unix(0, f.boostNow.Load()).UTC() }
	f.r.WithClock(now)
	f.social = socialpg.NewWithAuthority(f.db, f.guard)
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1::uuid,20,now()-interval '1 day')`, f.actor)
	slug := "boost-" + f.actor
	name := "Synthetic boost realm"
	yes := true
	var e error
	f.realm, e = f.social.CreateRealm(f.ctx, f.actor, social.RealmPatch{Slug: &slug, Name: &name, IsPublic: &yes, IsCommunity: &yes})
	if e != nil {
		t.Fatal(e)
	}
	walletFund(t, f.db, f.wallet, "golds", "100.000")
	walletFund(t, f.db, f.wallet, "points", "100000.000")
	f.cfg = realmboostpg.Config{TreasuryAccountID: f.actor, TreasuryWalletID: f.payee.ID, Policy: &realmboostpg.OwnedPolicy{LabelCaps: [4]int{0, 8, 16, 32}, PerkLevels: map[string]int{}}, Now: now}
	return configureRealmBoost(t, f)
}
func configureRealmBoost(t *testing.T, f realmBoostFixture) realmBoostFixture {
	t.Helper()
	var e error
	f.boosts, e = realmboostpg.New(f.db, f.guard, f.r, f.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.boosts.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.r.WithRealmBoosts(f.boosts)
	f.social = socialpg.NewWithAuthority(f.db, f.guard).WithBoosts(f.boosts)
	return f
}
func boostOrder(t *testing.T, f realmBoostFixture, shares int64, currency, key string) b.OrderResponse {
	t.Helper()
	out, e := f.boosts.CreateOrder(f.ctx, f.actor, f.realm.Slug, key, b.Request{Shares: shares, Currency: &currency})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func boostPay(t *testing.T, f realmBoostFixture, o b.OrderResponse) c.Order {
	t.Helper()
	paid, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID)
	if e != nil || paid.Status != c.Finished {
		t.Fatal(paid, e)
	}
	return paid
}
func TestPGRealmBoostCheckoutConservationAndReplay(t *testing.T) {
	f := newRealmBoostFixture(t)
	o := boostOrder(t, f, 10, "golds", "gold")
	if o.Amount.String() != "10.000" {
		t.Fatal(o)
	}
	again := boostOrder(t, f, 10, "golds", "gold")
	if again.OrderID != o.OrderID {
		t.Fatal("create replay")
	}
	if _, e := f.boosts.CreateOrder(f.ctx, f.actor, f.realm.ID, "gold", b.Request{Shares: 11}); !errors.Is(e, c.ErrIdempotency) {
		t.Fatal("rebound key", e)
	}
	st, e := f.boosts.Status(f.ctx, "", f.realm.ID)
	if e != nil || st.BoostPoints != 0 {
		t.Fatal("unpaid shares", st, e)
	}
	f.r.WithRealmBoosts(nil)
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal("missing fulfillment", e)
	}
	f.r.WithRealmBoosts(f.boosts)
	paid := boostPay(t, f, o)
	againPaid := boostPay(t, f, o)
	if paid.TransactionID == nil || againPaid.TransactionID == nil || *paid.TransactionID != *againPaid.TransactionID {
		t.Fatal("paid replay")
	}
	boostPay(t, f, boostOrder(t, f, 2, "points", "points"))
	st, e = f.boosts.Status(f.ctx, "", f.realm.ID)
	if e != nil || st.BoostPoints != 12 || st.BoostLevel != 1 || st.LabelCap != 8 {
		t.Fatal(st, e)
	}
	board, e := f.boosts.Leaderboard(f.ctx, "", f.realm.Slug, 20)
	if e != nil || len(board) != 1 || board[0].Shares != 12 || board[0].Boosts != 2 || board[0].Account == nil || board[0].AmountGolds.String() != "10.000" || board[0].AmountPoints.String() != "2000.000" {
		t.Fatal(board, e)
	}
	xp, e := f.boosts.Experience(f.ctx, "", f.realm.ID, f.actor, 50)
	if e != nil || len(xp) != 2 || xp[0].Delta+xp[1].Delta != 1200 {
		t.Fatal(xp, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "golds", "90.000", "0.000", "90.000")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "golds", "10.000", "0.000", "10.000")
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "points", "98000.000", "0.000", "98000.000")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "points", "2000.000", "0.000", "2000.000")
}
func TestPGRealmBoostRollbackAndBoundPayment(t *testing.T) {
	f := newRealmBoostFixture(t)
	o := boostOrder(t, f, 3, "golds", "")
	query(t, f.db, `CREATE FUNCTION synthetic_boost_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.actor_id::text=TG_ARGV[0] AND NEW.action='fulfilled' THEN RAISE EXCEPTION 'synthetic boost rollback';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_boost_fail BEFORE INSERT ON owned_realm_boost_audit FOR EACH ROW EXECUTE FUNCTION synthetic_boost_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_boost_fail ON owned_realm_boost_audit;DROP FUNCTION IF EXISTS synthetic_boost_fail()`)
	})
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); e == nil {
		t.Fatal("audit failure accepted")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "golds", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal("orphan XP")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal("orphan settlement")
	}
	query(t, f.db, `DROP TRIGGER synthetic_boost_fail ON owned_realm_boost_audit`)
	// Changing a canonical order's economics cannot buy the captured shares.
	query(t, f.db, `UPDATE commerce_v1.orders SET amount=1,document=jsonb_set(document,'{amount}','1'::jsonb)WHERE id=$1::uuid`, o.OrderID)
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("changed quote", e)
	}
	query(t, f.db, `UPDATE commerce_v1.orders SET amount=3,document=jsonb_set(document,'{amount}','3'::jsonb)WHERE id=$1::uuid`, o.OrderID)
	boostPay(t, f, o)
}
func TestPGRealmBoostCurrentMembershipPrivacyAndRevocation(t *testing.T) {
	f := newRealmBoostFixture(t)
	o := boostOrder(t, f, 2, "golds", "")
	if _, e := f.boosts.CreateOrder(context.Background(), f.actor, f.realm.ID, "", b.Request{Shares: 1}); e == nil {
		t.Fatal("asserted actor accepted")
	}
	query(t, f.db, `UPDATE owned_realms SET is_public=false WHERE id=$1::uuid`, f.realm.ID)
	if _, e := f.boosts.Status(f.ctx, "", f.realm.ID); !errors.Is(e, b.ErrNotFound) {
		t.Fatal("private anonymous read", e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=NULL WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, f.actor)
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("invited payment", e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET joined_at=now(),leave_at=now() WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, f.actor)
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("left payment", e)
	}
	query(t, f.db, `UPDATE owned_realm_members SET leave_at=NULL WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, f.actor)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("revoked boost permission", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "golds", "100.000", "0.000", "100.000")
}
func TestPGRealmBoostConcurrentSettlementAndLockConflict(t *testing.T) {
	f := newRealmBoostFixture(t)
	o := boostOrder(t, f, 5, "golds", "")
	done := make(chan error, 2)
	start := make(chan struct{})
	db2 := database(t)
	store2 := authoritypg.New(db2)
	v2, e := authority.NewVerifier(store2, f.config)
	if e != nil {
		t.Fatal(e)
	}
	p2, e := v2.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	g2 := authoritypg.NewTransactionGuard(store2, v2)
	ctx2 := g2.Bind(context.Background(), p2)
	r2 := commercepg.New(db2, g2, commercepg.Adapters{}).WithClock(f.cfg.Now)
	boost2, e := realmboostpg.New(db2, g2, r2, f.cfg)
	if e != nil {
		t.Fatal(e)
	}
	r2.WithRealmBoosts(boost2)
	for _, runner := range []func() error{func() error { _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); return e }, func() error { _, e := r2.PayOrder(ctx2, f.actor, o.OrderID, &f.wallet.ID); return e }} {
		go func(run func() error) { <-start; done <- run() }(runner)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid`, o.OrderID); n != 1 {
		t.Fatal("double XP", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "golds", "95.000", "0.000", "95.000")
	next := boostOrder(t, f, 2, "golds", "")
	blocker, _ := atomicBlocker(t, f.atomicAuthorityFixture, "owned_realms", f.realm.ID)
	if _, e := f.r.PayOrder(f.ctx, f.actor, next.OrderID, &f.wallet.ID); !errors.Is(e, c.ErrRetry) {
		t.Fatal("realm contention did not retry", e)
	}
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_realm_members SET leave_at=now() WHERE realm_id=$1::uuid AND account_id=$2::uuid`, f.realm.ID, f.actor); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.PayOrder(f.ctx, f.actor, next.OrderID, &f.wallet.ID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("stale membership after retry", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "golds", "95.000", "0.000", "95.000")
}
func TestPGRealmBoostExpiryAndPromotionEligibility(t *testing.T) {
	f := newRealmBoostFixture(t)
	order := boostOrder(t, f, 50, "golds", "expiry")
	boostPay(t, f, order)
	paidAt := f.boostNow.Load()
	invitee := newAtomicAuthorityFixture(t)
	existing := newAtomicAuthorityFixture(t)
	ordinary := newAtomicAuthorityFixture(t)
	for _, target := range []string{invitee.actor, existing.actor, ordinary.actor} {
		if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, target, []authority.Rule{{Pattern: "realms.*"}}, nil); e != nil {
			t.Fatal(e)
		}
		role := social.Moderator
		if target == ordinary.actor {
			role = social.Normal
		}
		if _, e := f.social.ChangeMember(f.ctx, f.actor, f.realm.ID, social.MemberChange{Action: social.Invite, AccountID: target, Role: role}); e != nil {
			t.Fatal(e)
		}
	}
	memberContext := func(actor string) context.Context {
		t.Helper()
		p, e := f.verifier.Authenticate(f.ctx, pgIssue(t, f.issuer, actor).Token)
		if e != nil {
			t.Fatal(e)
		}
		return f.guard.Bind(f.ctx, p)
	}
	existingContext := memberContext(existing.actor)
	if _, e := f.social.ChangeMember(existingContext, existing.actor, f.realm.ID, social.MemberChange{Action: social.Accept}); e != nil {
		t.Fatal("active elevated invitation", e)
	}
	permissionsBefore, e := f.social.EffectivePermissions(existingContext, existing.actor, f.realm.ID)
	if e != nil {
		t.Fatal(e)
	}
	f.boostNow.Store(paidAt + int64(30*24*time.Hour))
	st, e := f.boosts.Status(f.ctx, "", f.realm.ID)
	if e != nil || st.BoostLevel != 3 || st.BoostPoints != 50 {
		t.Fatal("inclusive cutoff", st, e)
	}
	visible, e := f.social.GetRealm(f.ctx, "", f.realm.ID)
	if e != nil || visible.BoostLevel != 3 || visible.BoostPoints != 50 {
		t.Fatal("realm projection", visible, e)
	}
	f.boostNow.Store(paidAt + int64(30*24*time.Hour+time.Microsecond))
	st, e = f.boosts.Status(f.ctx, "", f.realm.ID)
	if e != nil || st.BoostLevel != 0 || st.BoostPoints != 0 {
		t.Fatal("expired entitlement", st, e)
	}
	board, e := f.boosts.Leaderboard(f.ctx, "", f.realm.ID, 20)
	if e != nil || len(board) != 0 {
		t.Fatal("expired leaderboard", board, e)
	}
	// Replay returns the original order without extending the expired boost.
	boostPay(t, f, order)
	st, e = f.boosts.Status(f.ctx, "", f.realm.ID)
	if e != nil || st.BoostPoints != 0 {
		t.Fatal("paid replay extended expiry", st, e)
	}
	var fulfilled, expires time.Time
	if e = f.db.QueryRow(`SELECT fulfilled_at,expires_at FROM owned_realm_boost_purchases WHERE order_id=$1::uuid`, order.OrderID).Scan(&fulfilled, &expires); e != nil || fulfilled.UnixNano() != paidAt || !expires.Equal(fulfilled.Add(30*24*time.Hour)) {
		t.Fatal("paid replay changed original timestamps", fulfilled, expires, e)
	}
	if _, e = f.social.ChangeMember(memberContext(invitee.actor), invitee.actor, f.realm.ID, social.MemberChange{Action: social.Accept}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("expired elevated invitation activated", e)
	}
	if _, e = f.social.ChangeMember(memberContext(ordinary.actor), ordinary.actor, f.realm.ID, social.MemberChange{Action: social.Accept}); e != nil {
		t.Fatal("expiry blocked ordinary invitation", e)
	}
	current, e := f.social.Me(existingContext, existing.actor, f.realm.ID)
	if e != nil || current.Role != social.Moderator {
		t.Fatal("expiry removed an existing role", current, e)
	}
	permissionsAfter, e := f.social.EffectivePermissions(existingContext, existing.actor, f.realm.ID)
	if e != nil || permissionsBefore.Mask() != permissionsAfter.Mask() {
		t.Fatal("expiry changed unrelated permissions", permissionsBefore, permissionsAfter, e)
	}
	// A stale manually stored level must never keep a promotion privilege alive.
	query(t, f.db, `UPDATE owned_realms SET boost_level=3 WHERE id=$1::uuid`, f.realm.ID)
	other := newAtomicAuthorityFixture(t)
	if _, e := f.social.ChangeMember(f.ctx, f.actor, f.realm.ID, social.MemberChange{Action: social.Invite, AccountID: other.actor, Role: social.Moderator}); !errors.Is(e, social.ErrForbidden) {
		t.Fatal("expired promotion privilege", e)
	}
	xp, e := f.boosts.Experience(f.ctx, "", f.realm.ID, f.actor, 20)
	if e != nil || len(xp) != 1 || xp[0].Delta != 5000 {
		t.Fatal("XP incorrectly expired", xp, e)
	}
}
func TestPGRealmBoostCanonicalPerkSelection(t *testing.T) {
	f := newRealmBoostFixture(t)
	f.cfg.Policy.PerkLevels[f.plan] = 3
	f = configureRealmBoost(t, f)
	// Unpaid subscriptions confer no bonus.
	s, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: f.plan}, "perk", false)
	if e != nil {
		t.Fatal(e)
	}
	boostPay(t, f, boostOrder(t, f, 1, "golds", "base"))
	xp, e := f.boosts.Experience(f.ctx, "", f.realm.ID, f.actor, 20)
	if e != nil || len(xp) != 1 || xp[0].Delta != 100 {
		t.Fatal(xp, e)
	}
	so, e := f.r.SubscriptionOrder(f.ctx, f.actor, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, so.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	boostPay(t, f, boostOrder(t, f, 1, "golds", "perk"))
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE realm_id=$1::uuid AND delta=250 AND perk_level=3`, f.realm.ID); n != 1 {
		t.Fatal("missing canonical 2.5x perk", n)
	}
	// Queued and testing subscriptions retain their history but confer no current bonus.
	stored, e := f.r.Subscription(f.ctx, f.actor, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	future := stored
	future.BegunAt = f.cfg.Now().Add(time.Hour)
	raw, e := json.Marshal(future)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2::timestamptz,document=$3::jsonb WHERE id=$1::uuid`, s.ID, future.BegunAt, raw)
	boostPay(t, f, boostOrder(t, f, 1, "golds", "queued"))
	testingSub := stored
	testingSub.IsTesting = true
	raw, e = json.Marshal(testingSub)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2::timestamptz,document=$3::jsonb WHERE id=$1::uuid`, s.ID, stored.BegunAt, raw)
	boostPay(t, f, boostOrder(t, f, 1, "golds", "testing"))
	raw, e = json.Marshal(stored)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET document=$2::jsonb WHERE id=$1::uuid`, s.ID, raw)
	// Definition disable is current policy, even when the subscription remains active.
	query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1`, f.plan)
	boostPay(t, f, boostOrder(t, f, 1, "golds", "disabled"))
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE realm_id=$1::uuid AND delta=100 AND perk_level=0`, f.realm.ID); n != 4 {
		t.Fatal("disabled perk remained effective", n)
	}
	// An active status alone cannot retain a multiplier at or beyond its expiry.
	query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=true WHERE identifier=$1`, f.plan)
	f.boostNow.Store(stored.EndedAt.UnixNano() - int64(time.Microsecond))
	beforeExpiry := boostOrder(t, f, 1, "golds", "perk-before-expiry")
	boostPay(t, f, beforeExpiry)
	f.boostNow.Store(stored.EndedAt.UnixNano())
	atExpiry := boostOrder(t, f, 1, "golds", "perk-at-expiry")
	boostPay(t, f, atExpiry)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid AND delta=250 AND perk_level=3`, beforeExpiry.OrderID); n != 1 {
		t.Fatal("unexpired canonical perk was not applied", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid AND delta=100 AND perk_level=0`, atExpiry.OrderID); n != 1 {
		t.Fatal("expired canonical perk remained effective", n)
	}
	boostPay(t, f, beforeExpiry)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid AND delta=250 AND perk_level=3`, beforeExpiry.OrderID); n != 1 {
		t.Fatal("paid replay changed the original XP award", n)
	}
}
func TestPGRealmBoostRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "realm-boost-record.json"
	type saved struct {
		Actor, Realm, Order, Payer, Payee, Transaction string
		Token, Issuer, Audience, KeyID                 string
		PublicKey                                      []byte
		At, AuthorityAt                                int64
		AccessTTL                                      time.Duration
		Fulfilled, Expires                             time.Time
		Delta                                          int64
		Policy                                         realmboostpg.OwnedPolicy
	}
	if phase == "before" {
		f := newRealmBoostFixture(t)
		o := boostOrder(t, f, 10, "golds", "restart")
		paid := boostPay(t, f, o)
		if paid.TransactionID == nil {
			t.Fatal("missing original settlement")
		}
		v := saved{Actor: f.actor, Realm: f.realm.ID, Order: o.OrderID, Payer: f.wallet.ID, Payee: f.payee.ID, Transaction: *paid.TransactionID,
			Token: f.token, Issuer: f.config.Issuer, Audience: f.config.Audience, KeyID: f.config.SigningKeyID,
			At: f.boostNow.Load(), AuthorityAt: f.config.Now().UnixNano(), AccessTTL: f.config.AccessTTL, Policy: *f.cfg.Policy}
		var e error
		v.PublicKey, e = x509.MarshalPKIXPublicKey(f.config.TrustedKeys[v.KeyID])
		if e != nil {
			t.Fatal(e)
		}
		if e = db.QueryRow(`SELECT p.fulfilled_at,p.expires_at,x.delta FROM owned_realm_boost_purchases p JOIN owned_realm_boost_experience x ON x.id=p.id WHERE p.order_id=$1::uuid`, o.OrderID).Scan(&v.Fulfilled, &v.Expires, &v.Delta); e != nil {
			t.Fatal(e)
		}
		// Only synthetic bearer proof and public verification material are saved.
		// The shared helper creates a private exclusive marker in the disposable
		// cluster; the harness must never publish or upload its contents.
		saveRestartFixture(t, marker, v)
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	key, e := x509.ParsePKIXPublicKey(v.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	publicKey, ok := key.(*rsa.PublicKey)
	if !ok {
		t.Fatal("restart verification key is not RSA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// After restart, rebuild only adapters over the original rows and bearer
	// proof. No fixture constructor, account seed, grant or token issuance runs.
	config := authority.Config{Issuer: v.Issuer, Audience: v.Audience, TrustedKeys: map[string]*rsa.PublicKey{v.KeyID: publicKey}, AccessTTL: v.AccessTTL, Now: func() time.Time { return time.Unix(0, v.AuthorityAt).UTC() }}
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	principal, e := verifier.Authenticate(ctx, v.Token)
	if e != nil || principal.Account.ID != v.Actor {
		t.Fatal("original restart authority", e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	ctx = guard.Bind(ctx, principal)
	now := func() time.Time { return time.Unix(0, v.At).UTC() }
	checkout := commercepg.New(db, guard, commercepg.Adapters{}).WithClock(now)
	boosts, e := realmboostpg.New(db, guard, checkout, realmboostpg.Config{TreasuryAccountID: v.Actor, TreasuryWalletID: v.Payee, Policy: &v.Policy, Now: now})
	if e != nil {
		t.Fatal(e)
	}
	checkout.WithRealmBoosts(boosts)
	st, e := boosts.Status(ctx, v.Actor, v.Realm)
	if e != nil || st.BoostPoints != 10 || st.BoostLevel != 1 {
		t.Fatal(st, e)
	}
	paid, e := checkout.PayOrder(ctx, v.Actor, v.Order, &v.Payer)
	if e != nil || paid.Status != c.Finished || paid.TransactionID == nil || *paid.TransactionID != v.Transaction {
		t.Fatal("restart replaced original settlement", paid, e)
	}
	var fulfilled, expires time.Time
	var delta int64
	if e = db.QueryRow(`SELECT p.fulfilled_at,p.expires_at,x.delta FROM owned_realm_boost_purchases p JOIN owned_realm_boost_experience x ON x.id=p.id WHERE p.order_id=$1::uuid`, v.Order).Scan(&fulfilled, &expires, &delta); e != nil || !fulfilled.Equal(v.Fulfilled) || !expires.Equal(v.Expires) || delta != v.Delta {
		t.Fatal("restart changed original contribution or XP", e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid`, v.Order); n != 1 {
		t.Fatal("restart duplicated entitlement", n)
	}
	walletAssertBalance(t, walletpg.New(db), v.Payer, "golds", "90.000", "0.000", "90.000")
	walletAssertBalance(t, walletpg.New(db), v.Payee, "golds", "10.000", "0.000", "10.000")
}

func TestPGRealmBoostAuthorityRevocationAfterWait(t *testing.T) {
	f := newRealmBoostFixture(t)
	o := boostOrder(t, f, 2, "golds", "revocation")
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_authority_accounts", f.actor)
	done := make(chan error, 1)
	go func() { _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE id=$1::uuid`, f.principal.Session.ID); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("stale session after wait", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "golds", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal("revoked XP")
	}
}
func TestPGRealmBoostAuthenticatedHTTP(t *testing.T) {
	f := newRealmBoostFixture(t)
	auth := func(r *http.Request, required bool) (string, error) {
		if !required && r.Header.Get("Authorization") == "" {
			return "", nil
		}
		p, e := f.verifier.AuthenticateRequest(r)
		if e != nil {
			return "", e
		}
		*r = *r.WithContext(f.guard.Bind(r.Context(), p))
		return p.Account.ID, nil
	}
	h := b.NewHandler(f.boosts, auth)
	call := func(method, path, body, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	path := "/passport/realms/" + f.realm.Slug + "/boosts"
	call("POST", path, `{"shares":1}`, "", 401)
	w := call("POST", path, `{"shares":1,"currency":"points"}`, f.token, 200)
	var order b.OrderResponse
	if e := json.Unmarshal(w.Body.Bytes(), &order); e != nil {
		t.Fatal(e)
	}
	boostPay(t, f, order)
	call("GET", path, "", "", 200)
	call("GET", path+"/leaderboard?take=20", "", "", 200)
	call("GET", "/api/realms/"+f.realm.ID+"/members/"+f.actor+"/experience", "", "", 200)
	query(t, f.db, `UPDATE owned_realms SET is_public=false WHERE id=$1::uuid`, f.realm.ID)
	call("GET", path, "", "", 404)
	call("GET", path, "", f.token, 200)
	if e := f.issuer.Revoke(f.ctx, f.token, "current"); e != nil {
		t.Fatal(e)
	}
	call("GET", path, "", f.token, 401)
}
