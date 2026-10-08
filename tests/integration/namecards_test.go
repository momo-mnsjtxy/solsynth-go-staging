// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/autocompletepg"
	"solsynth.local/go-migration/internal/namecardpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/passport/social"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"testing"
)

type nameCardFixture struct {
	commerceFixture
	cards   *namecardpg.Repository
	options namecardpg.Options
	handler http.Handler
}

func newNameCardFixture(t *testing.T) nameCardFixture {
	t.Helper()
	f := nameCardFixture{commerceFixture: newCommerceFixture(t)}
	query(t, f.db, namecardpg.Schema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "accounts.manage"}, {Pattern: "publishers.update"}, {Pattern: "realms.update"}, {Pattern: "wallets.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.r.WithClock(f.config.Now)
	f.options = namecardpg.Options{Enabled: true, PricePoints: walletAmount(t, "10"), PointsCurrency: "synthetic-commerce", PayeeWalletID: f.payee.ID, PurchasePeriodDays: 30, MaxPurchases: 1, Now: f.config.Now}
	return configureNameCards(t, f)
}
func configureNameCards(t *testing.T, f nameCardFixture) nameCardFixture {
	t.Helper()
	social := socialpg.NewWithAuthority(f.db, f.guard)
	accounts := accountpg.NewWithAuthority(f.db, f.guard).WithRealms(social)
	var e error
	f.cards, e = namecardpg.New(f.db, f.guard, f.r, accounts, social, f.options)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.cards.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.r.WithNameCards(f.cards)
	f.handler = (&namecards.Service{Repository: f.cards, Authority: f.verifier}).Handler()
	return f
}
func paidNameCard(t *testing.T, f nameCardFixture, key string) namecards.OrderResponse {
	t.Helper()
	o, e := f.cards.Purchase(f.ctx, f.actor, key)
	if e != nil {
		t.Fatal(e)
	}
	f.r.WithNameCards(nil)
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal("payment without fulfillment adapter", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	f.r.WithNameCards(f.cards)
	paid, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID)
	if e != nil || paid.Status != c.Finished {
		t.Fatal(paid, e)
	}
	return o
}
func TestPGNameCardPaidAccountRenameAndIdempotency(t *testing.T) {
	f := newNameCardFixture(t)
	o, e := f.cards.Purchase(f.ctx, f.actor, "owned-order")
	if e != nil {
		t.Fatal(e)
	}
	if repeated, e := f.cards.Purchase(f.ctx, f.actor, "owned-order"); e != nil || repeated.PurchaseID != o.PurchaseID {
		t.Fatal("order retry duplicated", repeated, e)
	}
	if _, e = f.cards.Purchase(f.ctx, f.actor, "new-order"); !errors.Is(e, namecards.ErrPurchase) {
		t.Fatal("rolling purchase limit", e)
	}
	if _, e = f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "account", NewName: "UnpaidName"}, "unpaid-use"); !errors.Is(e, namecards.ErrUse) {
		t.Fatal("unpaid grant", e)
	}
	paid, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID)
	if e != nil || paid.Status != c.Finished {
		t.Fatal(paid, e)
	}
	if replay, e := f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); e != nil || replay.TransactionID == nil || *replay.TransactionID != *paid.TransactionID {
		t.Fatal("payment replay", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "90.000", "0.000", "90.000")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "10.000", "0.000", "10.000")
	newName := "Aa-" + strings.Repeat("b", 253)
	used, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "account", NewName: newName}, "owned-use")
	if e != nil || used.ConsumedAt == nil || used.NewName == nil || *used.NewName != newName {
		t.Fatal(used, e)
	}
	if _, e = f.verifier.Authenticate(f.ctx, f.token); e == nil {
		t.Fatal("rename retained stale version")
	}
	if a, e := authoritypg.New(f.db).LookupAccount(f.ctx, strings.ToUpper(newName)); e != nil || a.ID != f.actor {
		t.Fatal("canonical long-name lookup", a, e)
	}
	// Synthetic credential metadata verifies the same canonical sign-in lookup;
	// no new password, external contact or authentication authority is introduced.
	query(t, f.db, `INSERT INTO owned_authority_credentials(account_id,email,verified_at)VALUES($1::uuid,$2::text,now())`, f.actor, f.actor+"@example.invalid")
	if id, e := signinpg.New(f.db, authoritypg.New(f.db), f.guard).Lookup(f.ctx, strings.ToLower(newName)); e != nil || id != f.actor {
		t.Fatal("sign-in long-name lookup", id, e)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	p, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = f.guard.Bind(f.ctx, p)
	accountHandler := (&accounts.Service{Repository: accountpg.NewWithAuthority(f.db, f.guard), Authority: f.verifier}).Handler()
	settingsCall(t, accountHandler, "PATCH", "/stargate/accounts/me", f.token, fmt.Sprintf(`{"name":%q,"nick":"After paid rename"}`, newName), 200)
	settingsCall(t, accountHandler, "GET", "/stargate/accounts/"+newName, "", "", 200)
	autocomplete, e := autocompletepg.New(f.db, f.guard, autocompletepg.Config{})
	if e != nil {
		t.Fatal(e)
	}
	suggestions, e := autocomplete.Suggest(f.ctx, f.actor, "", "@"+newName)
	if e != nil || len(suggestions) != 1 || suggestions[0].Keyword != "@"+newName {
		t.Fatal("renamed account missing from autocomplete", suggestions, e)
	}
	if replay, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "account", NewName: newName}, "owned-use"); e != nil || replay.ID != used.ID {
		t.Fatal("use receipt did not replay", e)
	}
	if _, e = f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "account", NewName: "DifferentName"}, "owned-use"); !errors.Is(e, namecards.ErrIdempotency) {
		t.Fatal("key rebind", e)
	}
}
func TestPGNameCardPaymentAndRenameRollback(t *testing.T) {
	f := newNameCardFixture(t)
	o, e := f.cards.Purchase(f.ctx, f.actor, "")
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `CREATE FUNCTION synthetic_card_audit_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.account_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic card rollback'; END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_card_audit_fail BEFORE INSERT ON owned_name_card_audit FOR EACH ROW EXECUTE FUNCTION synthetic_card_audit_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_card_audit_fail ON owned_name_card_audit;DROP FUNCTION IF EXISTS synthetic_card_audit_fail()`)
	})
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); e == nil {
		t.Fatal("receipt/audit failure hidden")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_name_card_receipts WHERE purchase_id=$1::uuid`, o.PurchaseID); n != 0 {
		t.Fatal("receipt without settlement", n)
	}
	query(t, f.db, `DROP TRIGGER synthetic_card_audit_fail ON owned_name_card_audit`)
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.OrderID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_card_audit_fail BEFORE INSERT ON owned_name_card_audit FOR EACH ROW EXECUTE FUNCTION synthetic_card_audit_fail('%s')`, f.actor))
	if _, e = f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "account", NewName: "RollbackName"}, "rollback-use"); e == nil {
		t.Fatal("consume audit failure hidden")
	}
	if _, e = f.verifier.Authenticate(f.ctx, f.token); e != nil {
		t.Fatal("rename version partially committed", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_name_card_purchases WHERE id=$1::uuid AND consumed_at IS NULL`, o.PurchaseID); n != 1 {
		t.Fatal("card consumed on failed rename")
	}
}
func TestPGNameCardOneUseAndFreshPublisherAuthority(t *testing.T) {
	f := newNameCardFixture(t)
	paidNameCard(t, f, "race-order")
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_publishers", f.publisher)
	done := make(chan error, 1)
	go func() {
		_, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: "denied_" + f.actor}, "role-wait")
		done <- e
	}()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_publishers")
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_publisher_members SET role=50 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, f.publisher, f.actor); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("stale publisher role", e)
	}
	query(t, f.db, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, f.publisher, f.actor)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, suffix := range []string{"one", "two"} {
		go func(suffix string) {
			<-start
			_, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: "card_" + suffix + "_" + f.actor}, "race-"+suffix)
			results <- e
		}(suffix)
	}
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if !errors.Is(e, namecards.ErrUse) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("one card renamed twice", success)
	}
	accountRepo := accountpg.NewWithAuthority(f.db, f.guard)
	pub, e := accountRepo.LookupPublisherForViewer(f.ctx, f.actor, f.publisher)
	if e != nil {
		t.Fatal(e)
	}
	h := (&accounts.Service{Repository: accountRepo, Authority: f.verifier}).Handler()
	settingsCall(t, h, "PATCH", "/api/publishers/"+pub.Name, f.token, fmt.Sprintf(`{"name":%q,"nick":"After paid publisher rename"}`, pub.Name), 200)
	settingsCall(t, h, "PATCH", "/api/publishers/"+pub.Name, f.token, `{"name":"free_publisher_rename"}`, 409)
	if _, _, e := f.cards.List(context.Background(), f.actor, 10, 0); e == nil {
		t.Fatal("asserted identity read")
	}
}

func TestPGNameCardFederationBoundaryAndSourceFreeRealmSlug(t *testing.T) {
	f := newNameCardFixture(t)
	o := paidNameCard(t, f, "federation-order")
	query(t, f.db, `INSERT INTO owned_federation_publishers(publisher_id,enabled,manual,updated_at)VALUES($1::uuid,false,true,now())`, f.publisher)
	if _, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: "registered_" + f.actor}, "federation-use"); !errors.Is(e, namecards.ErrActorMigration) {
		t.Fatal("registered actor renamed", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_name_card_purchases WHERE id=$1::uuid AND consumed_at IS NULL`, o.PurchaseID); n != 1 {
		t.Fatal("unsupported rename spent card")
	}
	realm, member := authorityUUID(t), authorityUUID(t)
	slug := "source-free-" + realm
	query(t, f.db, `INSERT INTO owned_realms(id,slug,name,account_id,created_at,updated_at)VALUES($1::uuid,$2::text,'Synthetic realm',$3::uuid,now(),now())`, realm, slug, f.actor)
	query(t, f.db, `INSERT INTO owned_realm_members(id,realm_id,account_id,role,joined_at,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::uuid,100,now(),now(),now())`, member, realm, f.actor)
	changed := "source-kept-" + realm
	socialRepo := socialpg.NewWithAuthority(f.db, f.guard)
	if _, e := socialRepo.UpdateRealm(f.ctx, f.actor, realm, social.RealmPatch{Slug: &changed}); e != nil {
		t.Fatal("source free slug PATCH lost", e)
	}
	paidName := "source-card-" + realm
	if _, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "realm", TargetID: &realm, NewName: paidName}, "realm-use"); e != nil {
		t.Fatal("optional source owner card flow", e)
	}
}
func TestPGNameCardRestartPersistence(t *testing.T) {
	_ = database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "name-card-record.json")
	type saved struct{ Actor, Purchase, Order, Payer, Payee string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newNameCardFixture(t)
		o := paidNameCard(t, f, "restart")
		b, _ := json.Marshal(saved{f.actor, o.PurchaseID, o.OrderID, f.wallet.ID, f.payee.ID})
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var data saved
	if e = json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	f := newNameCardFixture(t)
	f.actor = data.Actor
	f.wallet.ID = data.Payer
	f.payee.ID = data.Payee
	f.options.PayeeWalletID = data.Payee
	f.token = pgIssue(t, f.issuer, f.actor).Token
	p, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = f.guard.Bind(f.ctx, p)
	f = configureNameCards(t, f)
	list, total, e := f.cards.List(f.ctx, f.actor, 100, 0)
	if e != nil || total != 1 || list[0].FulfilledAt == nil || list[0].ConsumedAt != nil {
		t.Fatal(list, total, e)
	}
	used, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "account", NewName: "restart_" + data.Actor}, "restart-use")
	if e != nil || used.ID != data.Purchase {
		t.Fatal(used, e)
	}
}
