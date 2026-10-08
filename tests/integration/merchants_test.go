// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/merchantpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"solsynth.local/go-migration/ports/wallet/merchants"
	"strings"
	"sync"
	"testing"
	"time"
)

func merchantRepo(t *testing.T, f commerceFixture) *merchantpg.Repository {
	t.Helper()
	query(t, f.db, merchantpg.Schema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	return merchantpg.New(f.db, f.guard).WithClock(func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() })
}
func merchantDefinition(f commerceFixture, key string) merchants.DefinitionInput {
	return merchants.DefinitionInput{Identifier: key, GroupIdentifier: key, DisplayName: "Synthetic catalog", Currency: "synthetic-commerce", BasePrice: json.Number("3.333"), CycleDurationDays: 1, PayeeWalletID: f.payee.ID, AllowedPaymentMethods: []string{"solian.wallet"}, AllowInternalWalletRenewal: boolean(true), AllowGift: boolean(true), ProviderMappings: map[string][]string{}}
}
func merchantWindow(f commerceFixture) merchants.Query {
	from, to := time.Unix(f.clock.Load(), 0).Add(-24*time.Hour), time.Unix(f.clock.Load(), 0).Add(time.Second)
	return merchants.Query{Take: 20, From: &from, To: &to}
}
func merchantOther(t *testing.T, f commerceFixture) (string, context.Context, wallet.Wallet) {
	t.Helper()
	id := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(f.ctx, f.db, id, "synthetic_merchant_other_"+strings.ReplaceAll(id, "-", ""), time.Unix(f.clock.Load(), 0)); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, f.issuer, id).Token
	v := f.verifier
	p, e := v.Authenticate(f.ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := f.guard.Bind(context.Background(), p)
	w := walletCreate(t, walletpg.New(f.db), id)
	return id, ctx, w
}
func TestPGMerchantsBindingOwnershipAndCanonicalCheckout(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	other, otherCtx, foreignWallet := merchantOther(t, f)
	if _, e := r.BindWallet(otherCtx, other, f.publisher, f.app, merchants.WalletPatch{WalletID: &foreignWallet.ID}); e == nil {
		t.Fatal("foreign publisher binding")
	}
	if _, e := r.BindWallet(f.ctx, f.actor, f.publisher, f.app, merchants.WalletPatch{WalletID: &foreignWallet.ID}); !errors.Is(e, merchants.ErrForbidden) {
		t.Fatal("foreign payout wallet", e)
	}
	fresh := walletCreate(t, walletpg.New(f.db), f.actor)
	old := commerceOrder(t, f, "old-payout", false)
	b, e := r.BindWallet(f.ctx, f.actor, f.publisher, f.app, merchants.WalletPatch{WalletID: &fresh.ID})
	if e != nil || b.PaymentWalletID == nil || *b.PaymentWalletID != fresh.ID {
		t.Fatal(b, e)
	}
	current := commerceOrder(t, f, "new-payout", false)
	if old.PayeeWalletID != f.payee.ID || current.PayeeWalletID != fresh.ID {
		t.Fatal("snapshot payout changed", old, current)
	}
	profile, e := r.Profile(f.ctx, f.actor, f.publisher)
	if e != nil || len(profile.Bindings) != 1 || profile.Bindings[0].AppID != f.app {
		t.Fatal(profile, e)
	}
	if _, e = r.BindWallet(f.ctx, f.actor, f.publisher, f.app, merchants.WalletPatch{}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Currency: "synthetic-commerce", Amount: walletAmount(t, "1")}, "unbound"); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("unbound checkout accepted", e)
	}
}
func TestPGMerchantsCatalogRoundTripAndDisable(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	key := "managed-" + f.app
	v := merchantDefinition(f, key)
	v.AppIdentifier = &f.app
	d, created, e := r.UpsertDefinition(f.ctx, f.actor, v)
	if e != nil || !created || d.AppIdentifier == nil || *d.AppIdentifier != "developer.app:"+f.app {
		t.Fatal(d, created, e)
	}
	catalog, e := f.r.Catalog(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, x := range catalog {
		if x.Identifier == key {
			found = true
			if x.BasePrice.String() != "3.333" || x.AppIdentifier == nil {
				t.Fatal(x)
			}
		}
	}
	if !found {
		t.Fatal("not in canonical catalog")
	}
	sub, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: key, PaymentMethod: "solian.wallet"}, "catalog-sub", false)
	if e != nil || sub.FinalPrice.String() != "3.333" {
		t.Fatal(sub, e)
	}
	v.BasePrice = json.Number("9.999")
	if _, created, e = r.UpsertDefinition(f.ctx, f.actor, v); e != nil || created {
		t.Fatal(created, e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, key)
	if e != nil || o.Amount.String() != "3.333" {
		t.Fatal("existing quote repriced", o, e)
	}
	if e = r.DeleteDefinition(f.ctx, f.actor, key); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.SubscriptionOrder(f.ctx, f.actor, key); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("disabled definition accepted", e)
	}
	retained, e := r.Definition(f.ctx, f.actor, key)
	if e != nil || retained.Enabled {
		t.Fatal("disable lost referenced history", retained, e)
	}
	if _, e = f.r.Subscription(f.ctx, f.actor, key); e != nil {
		t.Fatal("subscription destroyed", e)
	}
	if rows, n, e := r.Catalog(f.ctx, f.actor, merchants.Query{Take: 1, Offset: 100000}); e != nil || n < 2 || len(rows) != 0 {
		t.Fatal("paged total", rows, n, e)
	}
}
func TestPGMerchantsReportsExactAndIsolated(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	one := commerceOrder(t, f, "report-small", false)
	if _, e := f.r.PayOrder(f.ctx, f.actor, one.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Currency: "synthetic-commerce", Amount: walletAmount(t, "9007199254740993.123")}, "report-large"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Currency: "synthetic-other", Amount: walletAmount(t, "1.222")}, "report-other"); e != nil {
		t.Fatal(e)
	}
	q := merchantWindow(f)
	report, e := r.Report(f.ctx, f.actor, f.publisher, q)
	if e != nil || report.Count != 3 || report.ByCurrency["synthetic-commerce"].Amount.String() != "9007199254741003.124" || report.ByCurrency["synthetic-other"].Amount.String() != "1.222" {
		t.Fatal(report, e)
	}
	orders, n, e := r.Orders(f.ctx, f.actor, "", q)
	if e != nil || n != 1 || len(orders) != 1 || orders[0].ID != one.ID {
		t.Fatal("account order isolation", orders, n, e)
	}
	other, otherCtx, _ := merchantOther(t, f)
	if _, _, e = r.Orders(otherCtx, other, f.publisher, q); e == nil {
		t.Fatal("foreign merchant orders")
	}
	if got, n, e := r.Orders(otherCtx, other, "", q); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("foreign account orders", got, n, e)
	}
	// A different publisher under the same account may legitimately reuse the
	// payout wallet; publisher ancestry must still keep reports separate.
	name := "report-second-" + strings.ReplaceAll(f.app, "-", "")
	pub, e := accountpg.NewWithAuthority(f.db, f.guard).CreatePublisher(f.ctx, f.actor, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	dev, project, app, id := authorityUUID(t), authorityUUID(t), authorityUUID(t), authorityUUID(t)
	now := time.Unix(f.clock.Load(), 0).UTC()
	query(t, f.db, `INSERT INTO owned_developers(id,publisher_id,created_at) VALUES($1,$2,$3)`, dev, pub.ID, now)
	query(t, f.db, `INSERT INTO owned_dev_projects(id,developer_id,slug,name,created_at,updated_at) VALUES($1,$2,'report-second','Second', $3,$3)`, project, dev, now)
	query(t, f.db, `INSERT INTO owned_custom_apps(id,project_id,slug,name,status,created_at,updated_at) VALUES($1,$2,$3,'Second',1,$4,$4)`, app, project, name, now)
	order := c.Order{ID: id, AppIdentifier: "developer.app:" + app, Status: c.Unpaid, Currency: "synthetic-commerce", Amount: walletAmount(t, "100.000"), PayeeWalletID: f.payee.ID, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(time.Hour), Items: []c.OrderItem{}, Meta: map[string]json.RawMessage{}}
	raw, _ := json.Marshal(order)
	query(t, f.db, `INSERT INTO commerce_v1.orders(id,app_id,payee_wallet_id,status,amount,currency,created_at,expires_at,document) VALUES($1,$2,$3,0,$4,$5,$6,$7,$8)`, id, app, f.payee.ID, order.Amount, order.Currency, now, order.ExpiredAt, raw)
	report, e = r.Report(f.ctx, f.actor, f.publisher, q)
	if e != nil || report.Count != 3 {
		t.Fatal("shared wallet crossed publisher", report, e)
	}
	report, e = r.Report(f.ctx, f.actor, pub.ID, q)
	if e != nil || report.Count != 1 || report.ByCurrency["synthetic-commerce"].Amount.String() != "100.000" {
		t.Fatal(report, e)
	}
	to := one.CreatedAt
	q.To = &to
	if rows, n, e := r.Orders(f.ctx, f.actor, f.publisher, q); e != nil || n != 0 || len(rows) != 0 {
		t.Fatal("exclusive upper boundary", rows, n, e)
	}
}
func TestPGMerchantsCatalogConcurrentUpsertAndRollback(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	key := "race-" + f.app
	v := merchantDefinition(f, key)
	db2 := database(t)
	verifier, e := authority.NewVerifier(authoritypg.New(db2), f.config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db2), verifier)
	r2 := merchantpg.New(db2, guard).WithClock(func() time.Time { return time.Unix(f.clock.Load(), 0) })
	start := make(chan struct{})
	errs := make(chan error, 2)
	created := make(chan bool, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			repo, ctx := r, f.ctx
			if i == 1 {
				repo = r2
				ctx = guard.Bind(context.Background(), proof)
			}
			_, new, e := repo.UpsertDefinition(ctx, f.actor, v)
			created <- new
			errs <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if a, b := <-created, <-created; a == b {
		t.Fatal("upsert did not converge", a, b)
	}
	query(t, f.db, `ALTER TABLE owned_merchant_management_audit ADD CONSTRAINT merchant_catalog_failure CHECK(action<>'catalog.upsert') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_merchant_management_audit DROP CONSTRAINT IF EXISTS merchant_catalog_failure`)
	})
	v.BasePrice = json.Number("7.777")
	if _, _, e = r.UpsertDefinition(f.ctx, f.actor, v); e == nil {
		t.Fatal("audit failure hidden")
	}
	d, e := r.Definition(f.ctx, f.actor, key)
	if e != nil || d.BasePrice.String() != "3.333" {
		t.Fatal("partial catalog update", d, e)
	}
}
func TestPGMerchantsScopedCatalogRejectsDeadAncestry(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	key := "scoped-" + f.app
	v := merchantDefinition(f, key)
	v.AppIdentifier = &f.app
	if _, _, e := r.UpsertDefinition(f.ctx, f.actor, v); e != nil {
		t.Fatal(e)
	}
	other, otherCtx, _ := merchantOther(t, f)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	if _, e := f.r.CreateSubscription(otherCtx, other, c.CreateSubscription{Identifier: key, PaymentMethod: "solian.wallet"}, "disabled-owner", false); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("disabled owner accepted", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_publishers", f.publisher)
	done := make(chan error, 1)
	go func() {
		_, e := f.r.CreateSubscription(otherCtx, other, c.CreateSubscription{Identifier: key, PaymentMethod: "solian.wallet"}, "deleted-app", false)
		done <- e
	}()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_publishers")
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_custom_apps SET deleted_at=$2 WHERE id=$1`, f.app, time.Unix(f.clock.Load(), 0)); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, c.ErrNotFound) {
		t.Fatal("ancestry stale after wait", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.subscriptions WHERE account_id=$1 AND identifier=$2`, other, key); n != 0 {
		t.Fatal("partial dead-app subscription", n)
	}
}
func TestPGMerchantsAdminWaitRevocationAndHTTP(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	v := f.verifier
	h := merchants.NewVerifiedHandler(r, v)
	var e error
	if w := postHTTP(h, "GET", "/api/merchants/"+f.publisher, "", f.token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postHTTP(h, "PATCH", "/api/merchants/"+f.publisher+"/apps/"+f.app+"/wallet", `{}`, f.token); w.Code != 400 {
		t.Fatal("implicit unlink", w.Code, w.Body.String())
	}
	key := "revoked-" + f.app
	input := merchantDefinition(f, key)
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_authority_accounts", f.actor)
	done := make(chan error, 1)
	go func() { _, _, e := r.UpsertDefinition(f.ctx, f.actor, input); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
	if _, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=$2 WHERE account_id=$1`, f.actor, time.Unix(f.clock.Load(), 0)); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("revoked admin wrote catalog")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.definitions WHERE identifier=$1`, key); n != 0 {
		t.Fatal("revoked persisted", n)
	}
	if w := postHTTP(h, "GET", "/api/admin/subscriptions/catalog", "", f.token); w.Code != 401 {
		t.Fatal("revoked catalog read", w.Code)
	}
}

func TestPGMerchantsAdminScopeRecheckedAfterWait(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	key := "admin-scope-wait-" + f.app
	input := merchantDefinition(f, key)
	input.AppIdentifier = &f.app
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_publishers", f.publisher)
	done := make(chan error, 1)
	go func() { _, _, e := r.UpsertDefinition(f.ctx, f.actor, input); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_publishers")
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_custom_apps SET deleted_at=$2 WHERE id=$1`, f.app, time.Unix(f.clock.Load(), 0)); e != nil {
		t.Fatal(e)
	}
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, merchants.ErrNotFound) {
		t.Fatal("admin used stale app scope", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.definitions WHERE identifier=$1`, key); n != 0 {
		t.Fatal("dead app definition persisted", n)
	}
}
func TestPGMerchantsRestartPersistence(t *testing.T) {
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "merchants-restart.json")
	type record struct{ App, Wallet, Definition, Actor string }
	if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var ids record
		if e = json.Unmarshal(raw, &ids); e != nil {
			t.Fatal(e)
		}
		var bound string
		if e = db.QueryRow(`SELECT wallet_id::text FROM commerce_v1.merchants WHERE app_id=$1`, ids.App).Scan(&bound); e != nil || bound != ids.Wallet {
			t.Fatal(bound, e)
		}
		var doc []byte
		var enabled bool
		if e = db.QueryRow(`SELECT document,enabled FROM commerce_v1.definitions WHERE identifier=$1`, ids.Definition).Scan(&doc, &enabled); e != nil || !enabled {
			t.Fatal(e)
		}
		var d c.Definition
		if e = json.Unmarshal(doc, &d); e != nil || d.BasePrice.String() != "9007199254740993.123" || d.AppIdentifier == nil || *d.AppIdentifier != "developer.app:"+ids.App {
			t.Fatal("catalog restart", d, e)
		}
		if n := commerceCount(t, db, `SELECT count(*) FROM owned_merchant_management_audit WHERE actor_id=$1`, ids.Actor); n != 2 {
			t.Fatal("audit restart", n)
		}
		return
	}
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	fresh := walletCreate(t, walletpg.New(f.db), f.actor)
	if _, e := r.BindWallet(f.ctx, f.actor, f.publisher, f.app, merchants.WalletPatch{WalletID: &fresh.ID}); e != nil {
		t.Fatal(e)
	}
	key := "restart-managed-" + f.app
	v := merchantDefinition(f, key)
	v.PayeeWalletID = fresh.ID
	v.AppIdentifier = &f.app
	v.BasePrice = json.Number("9007199254740993.123")
	if _, _, e := r.UpsertDefinition(f.ctx, f.actor, v); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(record{f.app, fresh.ID, key, f.actor})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
