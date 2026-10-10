// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"solsynth.local/go-migration/ports/wallet/merchants"
	"strings"
	"testing"
	"time"
)

type shippingFixture struct {
	commerceFixture
	contacts     *signin.Service
	contact      signin.Contact
	signinConfig signin.Config
}

func newShippingFixture(t *testing.T) shippingFixture {
	t.Helper()
	f := newCommerceFixture(t)
	f.config.Now = func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() }
	var e error
	f.issuer, e = authority.New(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.verifier, e = authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
	proof, e := f.verifier.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.guard.Bind(context.Background(), proof), 45*time.Second)
	t.Cleanup(cancel)
	f.ctx = ctx
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "wallets.*"}, {Pattern: "account.contacts.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.r = commercepg.New(f.db, f.guard, commercepg.AppAdapters(apppg.New(f.db))).WithClock(f.config.Now)
	seal := sha256.Sum256([]byte("synthetic shipping contacts only"))
	cfg := signin.Config{Issuer: "Synthetic shipping", SealKey: seal[:], Now: f.config.Now, Delivery: &syntheticDelivery{tokens: map[string]string{}}}
	store := signinpg.New(f.db, authoritypg.New(f.db), f.guard)
	service, e := signin.New(store, f.issuer, f.verifier, cfg)
	if e != nil {
		t.Fatal(e)
	}
	address, e := service.CreateContact(f.ctx, f.token, signin.ContactInput{Type: 2, Content: "Synthetic Recipient, 1 Test Way, Example Town"})
	if e != nil {
		t.Fatal(e)
	}
	var product apps.Product
	var raw []byte
	if e = f.db.QueryRow(`SELECT document FROM owned_app_products WHERE id=$1`, f.product).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &product); e != nil {
		t.Fatal(e)
	}
	product.Fulfillment = &apps.Fulfillment{IsAddressRequired: true}
	raw, _ = json.Marshal(product)
	query(t, f.db, `UPDATE owned_app_products SET document=$2 WHERE id=$1`, f.product, raw)
	return shippingFixture{f, service, address, cfg}
}
func shippingRequest(f shippingFixture) c.CreateOrder {
	return c.CreateOrder{Credentials: f.credentials, Items: []c.ItemRequest{{ProductIdentifier: "synthetic-widget", Quantity: 2}}, Meta: map[string]json.RawMessage{"address_contact_id": json.RawMessage(fmt.Sprintf("%q", f.contact.ID)), "address_snapshot": json.RawMessage(`"Untrusted synthetic placeholder"`)}}
}
func shippingOrder(t *testing.T, f shippingFixture, key string) c.Order {
	t.Helper()
	o, e := f.r.CreateOrder(f.ctx, shippingRequest(f), key)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func shippingDestination(t *testing.T, o c.Order) c.Fulfillment {
	t.Helper()
	var v c.Fulfillment
	if e := json.Unmarshal(o.Meta["fulfillment"], &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func shippingScope(t *testing.T, f shippingFixture, scopes []string) {
	t.Helper()
	config, _ := json.Marshal(apps.OAuthConfig{AllowedScopes: []string{"orders.create", "orders.status.manage", "orders.metrics.read", c.FulfillmentReadScope}, RedirectURIs: []string{}, AllowedGrantTypes: []string{}})
	query(t, f.db, `UPDATE owned_custom_apps SET oauth_config=$2 WHERE id=$1`, f.app, config)
	raw, _ := json.Marshal(scopes)
	query(t, f.db, `UPDATE owned_app_secrets SET scopes=$2 WHERE id=$1`, f.secretID, raw)
}
func shippingFullScopes() []string {
	return []string{"orders.create", "orders.status.manage", "orders.metrics.read", c.FulfillmentReadScope}
}
func TestPGShippingAddressCRUDAndScopeIsolation(t *testing.T) {
	f := newShippingFixture(t)
	h := f.contacts.ContactHandler()
	path := "/stargate/contacts/" + f.contact.ID
	settingsCall(t, h, "GET", path, f.token, "", 200)
	body := `{"type":2,"content":"Synthetic Recipient, 2 Test Way, Example Town"}`
	settingsCall(t, h, "PATCH", path, f.token, body, 200)
	settingsCall(t, h, "PATCH", path, f.token, body, 200)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_contact_audit WHERE contact_id=$1 AND action='address.update'`, f.contact.ID); n != 1 {
		t.Fatal("address replay duplicated audit", n)
	}
	settingsCall(t, h, "PATCH", path, f.token, `{"type":0,"content":"synthetic@example.invalid"}`, 400)
	child, e := f.issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic unrelated scope", Scopes: []string{"posts.read"}})
	if e != nil {
		t.Fatal(e)
	}
	settingsCall(t, h, "GET", "/stargate/contacts", child.Token, "", 403)
	settingsCall(t, h, "GET", path, child.Token, "", 403)
	settingsCall(t, h, "PATCH", path, child.Token, body, 403)
	other, _, _ := merchantOther(t, f.commerceFixture)
	otherToken := pgIssue(t, f.issuer, other).Token
	// The shared contact service masks foreign/missing IDs as INVALID_CREDENTIALS.
	// Prove this token is valid independently, then preserve the denial contract.
	settingsCall(t, h, "GET", "/stargate/contacts", otherToken, "", 200)
	missingPath := "/stargate/contacts/" + authorityUUID(t)
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		requestBody := ""
		if method == "PATCH" {
			requestBody = body
		}
		foreign := settingsCall(t, h, method, path, otherToken, requestBody, 401)
		missing := settingsCall(t, h, method, missingPath, otherToken, requestBody, 401)
		if foreign.Body.String() != missing.Body.String() || strings.Contains(foreign.Body.String(), f.contact.ID) || strings.Contains(foreign.Body.String(), "Synthetic Recipient") {
			t.Fatal("foreign and missing contact denial must be indistinguishable and private")
		}
	}
	unchanged := backendDecode[signin.Contact](t, settingsCall(t, h, "GET", path, f.token, "", 200))
	if unchanged.Content != "Synthetic Recipient, 2 Test Way, Example Town" || unchanged.DeletedAt != nil || commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_contact_audit WHERE contact_id=$1 AND action='address.update'`, f.contact.ID) != 1 {
		t.Fatal("foreign attempts changed owned contact")
	}
	if e = f.contacts.SendContactVerification(f.ctx, f.token, f.contact.ID); !errors.Is(e, signin.ErrUnsupported) {
		t.Fatal("fake physical address verification", e)
	}
	if _, e = f.contacts.PublishContact(f.ctx, f.token, f.contact.ID, true); e == nil {
		t.Fatal("private address published")
	}
	settingsCall(t, h, "DELETE", path, f.token, "", 204)
	deleted := settingsCall(t, h, "GET", path, f.token, "", 401)
	missing := settingsCall(t, h, "GET", missingPath, f.token, "", 401)
	if deleted.Body.String() != missing.Body.String() || strings.Contains(deleted.Body.String(), f.contact.ID) || strings.Contains(deleted.Body.String(), "Synthetic Recipient") {
		t.Fatal("deleted contact existence or address disclosed")
	}
	replacement, e := f.contacts.CreateContact(f.ctx, f.token, signin.ContactInput{Type: 2, Content: "Synthetic Recipient, 2 Test Way, Example Town"})
	if e != nil || replacement.ID == f.contact.ID {
		t.Fatal("deleted address identity revived", e)
	}
}
func TestPGShippingCanonicalSnapshotAndFulfillment(t *testing.T) {
	f := newShippingFixture(t)
	o := shippingOrder(t, f, "shipping-snapshot")
	originalPayee := o.PayeeWalletID
	if v := shippingDestination(t, o); v.AddressSnapshot != "" || v.AddressContactID != "" || !v.RequiresAddress {
		t.Fatal("unverified app input became address", v)
	}
	updated, e := f.contacts.UpdateAddress(f.ctx, f.token, f.contact.ID, signin.ContactInput{Type: 2, Content: "Synthetic Recipient, 3 Test Way, Example Town"})
	if e != nil {
		t.Fatal(e)
	}
	// Later catalog and payout edits do not change this order's economics.
	query(t, f.db, `UPDATE owned_app_products SET document=jsonb_set(document,'{price}','99.999'::jsonb) WHERE id=$1`, f.product)
	rebound := walletCreate(t, walletpg.New(f.db), f.actor)
	manager := merchantRepo(t, f.commerceFixture)
	if _, e = manager.BindWallet(f.ctx, f.actor, f.publisher, f.app, merchants.WalletPatch{WalletID: &rebound.ID}); e != nil {
		t.Fatal(e)
	}
	paid, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
	if e != nil || paid.Status != c.Paid || paid.Amount.String() != "4.247" || paid.PayeeWalletID != originalPayee {
		t.Fatal(paid, e)
	}
	v := shippingDestination(t, paid)
	if v.AddressSnapshot != updated.Content || v.AddressContactID != f.contact.ID {
		t.Fatal(v)
	}
	if e = f.contacts.RemoveContact(f.ctx, f.token, f.contact.ID); e != nil {
		t.Fatal(e)
	}
	replay, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
	if e != nil || *replay.TransactionID != *paid.TransactionID || shippingDestination(t, replay) != v {
		t.Fatal("paid address replay changed", e)
	}
	owner, e := f.r.GetOrder(f.ctx, f.actor, o.ID, false)
	if e != nil || shippingDestination(t, owner) != v {
		t.Fatal(e)
	}
	public, e := f.r.GetOrder(context.Background(), "", o.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(public)
	if strings.Contains(string(encoded), updated.Content) || strings.Contains(string(encoded), f.contact.ID) {
		t.Fatal("public address leak")
	}
	created, e := f.r.CreateOrder(f.ctx, shippingRequest(f), "shipping-snapshot")
	if e != nil || shippingDestination(t, created).AddressSnapshot != "" {
		t.Fatal("create-only replay leaked canonical address", e)
	}
	if _, e = f.r.OrderFulfillment(f.ctx, o.ID, f.credentials); e == nil {
		t.Fatal("status/create scope read delivery address")
	}
	shippingScope(t, f, []string{c.FulfillmentReadScope})
	delivery, e := f.r.OrderFulfillment(f.ctx, o.ID, f.credentials)
	if e != nil || delivery.AddressSnapshot != updated.Content {
		t.Fatal(delivery, e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e == nil {
		t.Fatal("read-only fulfillment scope changed settlement")
	}
	shippingScope(t, f, shippingFullScopes())
	merchant := merchantRepo(t, f.commerceFixture)
	rows, total, e := merchant.Orders(f.ctx, f.actor, f.publisher, merchantWindow(f.commerceFixture))
	if e != nil || total != 1 || len(rows) != 1 {
		t.Fatal(rows, total, e)
	}
	raw, _ := json.Marshal(rows)
	if !strings.Contains(string(raw), updated.Content) || strings.Contains(string(raw), f.contact.ID) || strings.Contains(string(raw), "payer_wallet_id") {
		t.Fatal("merchant projection not limited", string(raw))
	}
	finished, e := f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished})
	if e != nil || finished.Status != c.Finished || shippingDestination(t, finished).AddressSnapshot != "" {
		t.Fatal(finished, e)
	}
	if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e != nil {
		t.Fatal(e)
	}
	if released, settleErr := merchant.Settle(f.ctx, f.actor, f.publisher); settleErr != nil || len(released) != 1 {
		t.Fatal("explicit shipping settlement", released, settleErr)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "0.000", "95.753")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "4.247", "0.000", "4.247")
	walletAssertBalance(t, walletpg.New(f.db), rebound.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND payload::text LIKE $2`, o.ID, "%"+updated.Content+"%"); n != 0 {
		t.Fatal("general outbox leaked address", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 1 {
		t.Fatal("replay duplicated transfer", n)
	}
}
func TestPGShippingOwnershipAndRequiredAddressValidation(t *testing.T) {
	f := newShippingFixture(t)
	for _, meta := range []map[string]json.RawMessage{nil, {"address_contact_id": json.RawMessage(`"bad"`), "address_snapshot": json.RawMessage(`"Synthetic"`)}, {"address_contact_id": json.RawMessage(fmt.Sprintf("%q", f.contact.ID))}, {"fulfillment": json.RawMessage(`{"requires_address":false}`)}} {
		req := shippingRequest(f)
		req.Meta = meta
		if _, e := f.r.CreateOrder(f.ctx, req, ""); e == nil {
			t.Fatal("missing or forged destination accepted")
		}
	}
	o := shippingOrder(t, f, "foreign-address")
	other, otherCtx, wallet := merchantOther(t, f.commerceFixture)
	walletFund(t, f.db, wallet, "synthetic-commerce", "10.000")
	if _, e := f.r.PayOrder(otherCtx, other, o.ID, &wallet.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("foreign address selected", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.order_destinations WHERE order_id=$1 AND captured_at IS NOT NULL`, o.ID); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 0 {
		t.Fatal("foreign checkout moved funds", n)
	}
	foreign := newCommerceFixture(t)
	shippingScope(t, shippingFixture{commerceFixture: foreign}, shippingFullScopes())
	if _, e := f.r.OrderFulfillment(f.ctx, o.ID, foreign.credentials); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("cross-app delivery view")
	}
	if _, e := f.r.GetOrder(otherCtx, other, o.ID, false); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestPGShippingAddressMutationCheckoutRaces(t *testing.T) {
	t.Run("delete_before_checkout", func(t *testing.T) {
		f := newShippingFixture(t)
		o := shippingOrder(t, f, "delete-before")
		ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
		defer cancel()
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
		if _, e := blocker.ExecContext(ctx, `UPDATE owned_authority_contacts SET deleted_at=clock_timestamp() WHERE id=$1`, f.contact.ID); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { _, e := f.r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID); done <- e }()
		waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e := <-done; !errors.Is(e, c.ErrNotFound) {
			t.Fatal("deleted address admitted", e)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, o.ID); n != 0 {
			t.Fatal(n)
		}
	})
	t.Run("checkout_precedes_edit", func(t *testing.T) {
		f := newShippingFixture(t)
		o := shippingOrder(t, f, "pay-before-edit")
		ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
		defer cancel()
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "wallet_v1.wallets", f.wallet.ID)
		type paidResult struct {
			o c.Order
			e error
		}
		paid := make(chan paidResult, 1)
		go func() { o, e := f.r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID); paid <- paidResult{o, e} }()
		payerPID := waitAtomicBlocked(t, locks, pid, "wallet_v1.wallets")
		edit := make(chan error, 1)
		go func() {
			_, e := f.contacts.UpdateAddress(ctx, f.token, f.contact.ID, signin.ContactInput{Type: 2, Content: "Synthetic Recipient, Later Test Way"})
			edit <- e
		}()
		waitAtomicBlocked(t, locks, payerPID, "owned_authority_accounts")
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		v := <-paid
		if v.e != nil {
			t.Fatal(v.e)
		}
		if e := <-edit; e != nil {
			t.Fatal(e)
		}
		if shippingDestination(t, v.o).AddressSnapshot != f.contact.Content {
			t.Fatal("edit changed captured checkout")
		}
		current, e := f.contacts.GetContact(ctx, f.token, f.contact.ID)
		if e != nil || current.Content == f.contact.Content {
			t.Fatal("edit did not run after checkout", e)
		}
	})
}
func TestPGShippingCurrentAuthorityAfterWaits(t *testing.T) {
	for _, operation := range []string{"payment", "address_edit"} {
		t.Run(operation, func(t *testing.T) {
			f := newShippingFixture(t)
			o := shippingOrder(t, f, "revocation")
			permission := "orders.pay"
			if operation == "address_edit" {
				permission = "account.contacts.manage"
			}
			starts := f.config.Now().Add(2 * time.Second)
			if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "orders.*"}, {Pattern: "wallets.*"}, {Pattern: "account.contacts.manage"}}, []authority.Punishment{{Kind: "permission_modification", Pattern: permission, StartsAt: &starts}}); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_contacts", f.contact.ID)
			done := make(chan error, 1)
			go func() {
				var e error
				if operation == "payment" {
					_, e = f.r.PayOrder(ctx, f.actor, o.ID, &f.wallet.ID)
				} else {
					_, e = f.contacts.UpdateAddress(ctx, f.token, f.contact.ID, signin.ContactInput{Type: 2, Content: "Synthetic forbidden edit"})
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_authority_contacts")
			f.clock.Add(3)
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, authority.ErrForbidden) {
				t.Fatal("after-wait permission accepted", operation, e)
			}
			var text string
			if e := f.db.QueryRow(`SELECT content FROM owned_authority_contacts WHERE id=$1`, f.contact.ID).Scan(&text); e != nil || text != f.contact.Content {
				t.Fatal("failed edit survived", text, e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.order_destinations WHERE order_id=$1 AND captured_at IS NOT NULL`, o.ID); n != 0 {
				t.Fatal("failed payment snapshot survived", n)
			}
		})
	}
}
func TestPGShippingAppAndMerchantScopeIsolation(t *testing.T) {

	t.Run("expiry_after_order_wait", func(t *testing.T) {
		f := newShippingFixture(t)
		o := shippingOrder(t, f, "late-app-expiry")
		if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
			t.Fatal(e)
		}
		shippingScope(t, f, shippingFullScopes())
		ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
		defer cancel()
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "commerce_v1.orders", o.ID)
		query(t, f.db, `UPDATE owned_app_secrets SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, f.secretID)
		type outcome struct {
			v c.MerchantFulfillment
			e error
		}
		done := make(chan outcome, 1)
		go func() { v, e := f.r.OrderFulfillment(ctx, o.ID, f.credentials); done <- outcome{v, e} }()
		waitAtomicBlocked(t, locks, pid, "commerce_v1.orders")
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			var expired bool
			if e := f.db.QueryRowContext(ctx, `SELECT expires_at<=clock_timestamp() FROM owned_app_secrets WHERE id=$1`, f.secretID).Scan(&expired); e != nil {
				t.Fatal(e)
			}
			if expired {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-ticker.C:
			}
		}
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		result := <-done
		if result.e == nil || result.v.AddressSnapshot != "" {
			t.Fatal("expired app secret retained destination", result.e)
		}
	})
	f := newShippingFixture(t)
	o := shippingOrder(t, f, "app-scope")
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	shippingScope(t, f, shippingFullScopes())
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_app_secrets", f.secretID)
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_app_secrets SET revoked_at=clock_timestamp() WHERE id=$1`, f.secretID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.r.OrderFulfillment(ctx, o.ID, f.credentials); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_app_secrets")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("revoked app secret read destination")
	}
	repo := merchantRepo(t, f.commerceFixture)
	scoped, e := f.issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic insufficient report scope", Scopes: []string{"posts.read"}})
	if e != nil {
		t.Fatal(e)
	}
	proof, e := f.verifier.Authenticate(f.ctx, scoped.Token)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = repo.Orders(f.guard.Bind(context.Background(), proof), f.actor, f.publisher, merchantWindow(f.commerceFixture)); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("publisher role bypassed order-view scope", e)
	}
	other, otherCtx, _ := merchantOther(t, f.commerceFixture)
	if _, _, e = repo.Orders(otherCtx, other, f.publisher, merchantWindow(f.commerceFixture)); e == nil {
		t.Fatal("foreign merchant accessed order address")
	}
}
func TestPGShippingRollbackAndImmutableDestination(t *testing.T) {
	f := newShippingFixture(t)
	o := shippingOrder(t, f, "rollback")
	query(t, f.db, `CREATE FUNCTION synthetic_shipping_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.managed_by::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic checkout rollback'; END IF; RETURN NEW; END $$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_shipping_failure BEFORE INSERT ON wallet_v1.transactions FOR EACH ROW EXECUTE FUNCTION synthetic_shipping_failure('%s')`, o.ID))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_shipping_failure ON wallet_v1.transactions;DROP FUNCTION IF EXISTS synthetic_shipping_failure()`)
	})
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e == nil {
		t.Fatal("injected checkout failure ignored")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.order_destinations WHERE order_id=$1 AND captured_at IS NOT NULL`, o.ID); n != 0 {
		t.Fatal("snapshot survived failed payment", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	query(t, f.db, `DROP TRIGGER synthetic_shipping_failure ON wallet_v1.transactions;DROP FUNCTION synthetic_shipping_failure()`)
	if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(`UPDATE commerce_v1.order_destinations SET address_snapshot='Synthetic altered paid address' WHERE order_id=$1`, o.ID); e == nil {
		t.Fatal("paid destination rewritten")
	}
	if _, e := f.db.Exec(`UPDATE commerce_v1.orders SET document=jsonb_set(document,'{meta,fulfillment,address_snapshot}','"Synthetic altered paid address"') WHERE id=$1`, o.ID); e == nil {
		t.Fatal("paid destination projection rewritten")
	}
	if _, e := f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
}
func TestPGBackendShippingAddressCheckout(t *testing.T) {
	f := newShippingFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Signin: &f.signinConfig, SessionService: f.issuer})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "PATCH", "/stargate/contacts/"+f.contact.ID, f.token, `{"type":2,"content":"Synthetic Recipient, Host Test Way"}`, 200)
	raw, _ := json.Marshal(shippingRequest(f))
	o := backendDecode[c.Order](t, backendCall(t, app, "POST", "/wallet/orders", "", string(raw), 200))
	payBody := fmt.Sprintf(`{"payer_wallet_id":%q}`, f.wallet.ID)
	backendCall(t, app, "POST", "/wallet/orders/"+o.ID+"/pay", f.token, payBody, 503)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1,false,$2)`, f.actor, f.config.Now())
	paid := backendDecode[c.Order](t, backendCall(t, app, "POST", "/wallet/orders/"+o.ID+"/pay", f.token, payBody, 200))
	if shippingDestination(t, paid).AddressSnapshot != "Synthetic Recipient, Host Test Way" {
		t.Fatal(paid)
	}
	private := backendCall(t, app, "GET", "/wallet/orders/mine/"+o.ID, f.token, "", 200)
	if !strings.Contains(private.Body.String(), "Host Test Way") {
		t.Fatal("buyer address missing")
	}
	public := backendCall(t, app, "GET", "/wallet/orders/"+o.ID, "", "", 200)
	if strings.Contains(public.Body.String(), "Host Test Way") {
		t.Fatal("public address leak")
	}
	shippingScope(t, f, shippingFullScopes())
	creds, _ := json.Marshal(f.credentials)
	limited := backendCall(t, app, "POST", "/wallet/orders/"+o.ID+"/fulfillment", "", string(creds), 200)
	if !strings.Contains(limited.Body.String(), "Host Test Way") || strings.Contains(limited.Body.String(), f.contact.ID) {
		t.Fatal(limited.Body.String())
	}
}
func TestPGShippingRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	const marker = "shipping-destination.json"
	type saved struct {
		Order, Account, Contact, Text, Payee, Transaction string
		Revision                                          int64
		Captured                                          time.Time
	}
	if phase == "before" {
		f := newShippingFixture(t)
		o := shippingOrder(t, f, "restart")
		paid, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.contacts.RemoveContact(f.ctx, f.token, f.contact.ID); e != nil {
			t.Fatal(e)
		}
		v := saved{Order: o.ID, Account: f.actor, Contact: f.contact.ID, Payee: o.PayeeWalletID, Transaction: *paid.TransactionID}
		if e = f.db.QueryRow(`SELECT address_snapshot,contact_revision,captured_at FROM commerce_v1.order_destinations WHERE order_id=$1`, o.ID).Scan(&v.Text, &v.Revision, &v.Captured); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, v)
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	db := database(t)
	var current saved
	var deleted bool
	var data []byte
	if e := db.QueryRow(`SELECT order_id::text,account_id::text,address_contact_id::text,address_snapshot,contact_revision,captured_at FROM commerce_v1.order_destinations WHERE order_id=$1`, v.Order).Scan(&current.Order, &current.Account, &current.Contact, &current.Text, &current.Revision, &current.Captured); e != nil {
		t.Fatal(e)
	}
	if current.Order != v.Order || current.Account != v.Account || current.Contact != v.Contact || current.Text != v.Text || current.Revision != v.Revision || !current.Captured.Equal(v.Captured) {
		t.Fatal("saved destination changed across restart")
	}
	if e := db.QueryRow(`SELECT document,payee_wallet_id::text,transaction_id::text FROM commerce_v1.orders WHERE id=$1`, v.Order).Scan(&data, &current.Payee, &current.Transaction); e != nil {
		t.Fatal(e)
	}
	var order c.Order
	if e := json.Unmarshal(data, &order); e != nil {
		t.Fatal(e)
	}
	if shippingDestination(t, order).AddressSnapshot != v.Text || current.Payee != v.Payee || current.Transaction != v.Transaction || order.Amount.String() != "4.247" {
		t.Fatal("order snapshot/economics changed")
	}
	if e := db.QueryRow(`SELECT deleted_at IS NOT NULL FROM owned_authority_contacts WHERE id=$1`, v.Contact).Scan(&deleted); e != nil || !deleted {
		t.Fatal("deleted contact resurrected", e)
	}
}
