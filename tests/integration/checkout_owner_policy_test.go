// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
	"time"
)

type checkoutOwnerFixture struct {
	shippingFixture
	buyer    string
	buyerCtx context.Context
	wallet   wallet.Wallet
	order    c.Order
}

func newCheckoutOwnerFixture(t *testing.T) checkoutOwnerFixture {
	t.Helper()
	f := newShippingFixture(t)
	buyer, _, payer := merchantOther(t, f.commerceFixture)
	token := pgIssue(t, f.issuer, buyer).Token
	principal, e := f.verifier.Authenticate(f.ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := f.guard.Bind(f.ctx, principal)
	contact, e := f.contacts.CreateContact(ctx, token, signin.ContactInput{Type: 2, Content: "Synthetic Other Buyer, Checkout Policy Way"})
	if e != nil {
		t.Fatal(e)
	}
	walletFund(t, f.db, payer, "synthetic-commerce", "100.000")
	request := shippingRequest(f)
	request.Meta["address_contact_id"], _ = json.Marshal(contact.ID)
	o, e := f.r.CreateOrder(f.ctx, request, "owner-policy")
	if e != nil {
		t.Fatal(e)
	}
	// Exercise the host's real internal payment proof and policy composition.
	payment := walletpg.NewPINVerifier(f.db, nil).WithAuthority(f.guard).WithClock(f.config.Now)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1,false,$2)`, buyer, f.config.Now())
	ctx, e = payment.Atomic().PreparePayment(ctx, wallet.Principal{AccountID: buyer, SessionID: principal.Session.ID}, "", wallet.PaymentOrder, wallet.OrderPaymentBinding(o.ID, &payer.ID))
	if e != nil {
		t.Fatal(e)
	}
	f.r.WithPaymentAuthorization(payment)
	return checkoutOwnerFixture{f, buyer, ctx, payer, o}
}

func assertCheckoutOwnerRollback(t *testing.T, f checkoutOwnerFixture) {
	t.Helper()
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	for _, q := range []string{
		`SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`,
		`SELECT count(*) FROM commerce_v1.order_destinations WHERE order_id=$1 AND captured_at IS NOT NULL`,
		`SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1 AND event_type='commerce.order.paid'`,
	} {
		if n := commerceCount(t, f.db, q, f.order.ID); n != 0 {
			t.Fatal("rejected checkout retained an effect", n)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders WHERE id=$1 AND status=0 AND transaction_id IS NULL AND payer_wallet_id IS NULL`, f.order.ID); n != 1 {
		t.Fatal("rejected checkout bound the order", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1 AND consumed_at IS NOT NULL`, f.buyer); n != 0 {
		t.Fatal("rejected checkout consumed payment proof", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.outbox WHERE payload->>'payer_wallet_id'=$1::text AND event_type='wallet.transaction.created'`, f.wallet.ID); n != 0 {
		t.Fatal("rejected checkout retained a wallet event", n)
	}
}

func TestPGCheckoutOwnerEligibilityAndPaidReplay(t *testing.T) {
	for _, state := range []string{"disabled", "active_punishment", "future_punishment", "expired_punishment", "healthy"} {
		t.Run(state, func(t *testing.T) {
			f := newCheckoutOwnerFixture(t)
			switch state {
			case "disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "active_punishment":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, f.actor)
			case "future_punishment":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()+interval '1 hour')`, f.actor)
			case "expired_punishment":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,expires_at)VALUES($1,'disable_account',clock_timestamp()-interval '1 second')`, f.actor)
			}
			paid, e := f.r.PayOrder(f.buyerCtx, f.buyer, f.order.ID, &f.wallet.ID)
			if state == "disabled" || state == "active_punishment" {
				if !errors.Is(e, c.ErrForbidden) {
					t.Fatal("disabled owner accepted a new internal charge", e)
				}
				assertCheckoutOwnerRollback(t, f)
				return
			}
			if e != nil || paid.Status != c.Paid {
				t.Fatal("eligible owner checkout failed", e)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "4.247", "95.753")
			if state != "healthy" {
				return
			}
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			// A fresh real buyer proof still permits reading the already paid result.
			// The payment proof is per operation; issue a new one for paid replay.
			payment := walletpg.NewPINVerifier(f.db, nil).WithAuthority(f.guard).WithClock(f.config.Now)
			token := pgIssue(t, f.issuer, f.buyer).Token
			p, e := f.verifier.Authenticate(f.ctx, token)
			if e != nil {
				t.Fatal(e)
			}
			ctx := f.guard.Bind(f.ctx, p)
			ctx, e = payment.Atomic().PreparePayment(ctx, wallet.Principal{AccountID: f.buyer, SessionID: p.Session.ID}, "", wallet.PaymentOrder, wallet.OrderPaymentBinding(f.order.ID, &f.wallet.ID))
			if e != nil {
				t.Fatal(e)
			}
			f.r.WithPaymentAuthorization(payment)
			replay, e := f.r.PayOrder(ctx, f.buyer, f.order.ID, &f.wallet.ID)
			if e != nil || replay.TransactionID == nil || *replay.TransactionID != *paid.TransactionID {
				t.Fatal("owner disable changed paid receipt replay", e)
			}
			// Existing app credential authorization still controls refunds. Once
			// re-enabled, cancellation releases the original held transaction.
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
			if _, e = f.r.UpdateOrder(f.ctx, f.order.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Cancelled}); e != nil {
				t.Fatal(e)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
		})
	}
}

func TestPGCheckoutOwnerDisableAfterWaitRollsBack(t *testing.T) {
	for _, boundary := range []string{"app", "wallet"} {
		for _, state := range []string{"disabled", "scheduled_punishment"} {
			t.Run(boundary+"_"+state, func(t *testing.T) {
				f := newCheckoutOwnerFixture(t)
				if state == "scheduled_punishment" {
					query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp()+interval '1 hour')`, f.actor)
				}
				ctx, cancel := context.WithTimeout(f.buyerCtx, 15*time.Second)
				defer cancel()
				locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
				table, id := "wallet_v1.wallets", f.wallet.ID
				if boundary == "app" {
					table, id = "owned_custom_apps", f.app
				}
				blocker, pid := atomicBlocker(t, locks, table, id)
				done := make(chan error, 1)
				go func() { _, e := f.r.PayOrder(ctx, f.buyer, f.order.ID, &f.wallet.ID); done <- e }()
				waitAtomicBlocked(t, locks, pid, table)
				if state == "disabled" {
					// The canonical policy writer locks only the merchant owner account;
					// checkout must not hold it behind the already-held buyer account.
					if e := authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); e != nil {
						t.Fatal(e)
					}
				} else {
					// Backdate only this synthetic schedule. The global maintenance and
					// authority clocks stay unchanged, and earlier checks saw the future.
					query(t, f.db, `UPDATE owned_authority_punishments SET starts_at=clock_timestamp()-interval '1 second' WHERE account_id=$1 AND kind='disable_account'`, f.actor)
				}
				if e := blocker.Commit(); e != nil {
					t.Fatal(e)
				}
				if e := <-done; !errors.Is(e, c.ErrForbidden) {
					t.Fatal("post-wait disabled owner accepted charge", e)
				}
				assertCheckoutOwnerRollback(t, f)
			})
		}
	}
}

func TestPGCheckoutRealmOwnerEligibility(t *testing.T) {
	for _, state := range []string{"healthy", "deleted_realm", "disabled_owner", "punished_owner", "changed_owner_healthy", "changed_owner_disabled"} {
		t.Run(state, func(t *testing.T) {
			f := newCheckoutOwnerFixture(t)
			realm := authorityUUID(t)
			query(t, f.db, `INSERT INTO owned_realms(id,slug,name,account_id,created_at,updated_at)VALUES($1,$2,'Synthetic checkout realm',$3,clock_timestamp(),clock_timestamp())`, realm, "checkout-"+realm, f.actor)
			query(t, f.db, `UPDATE owned_publishers SET type=1,account_id=NULL,realm_id=$2 WHERE id=$1`, f.publisher, realm)
			switch state {
			case "deleted_realm":
				query(t, f.db, `UPDATE owned_realms SET deleted_at=clock_timestamp() WHERE id=$1`, realm)
			case "disabled_owner":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "punished_owner":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, f.actor)
			case "changed_owner_healthy", "changed_owner_disabled":
				owner := authorityUUID(t)
				if e := authoritypg.SeedSynthetic(context.Background(), f.db, owner, "synthetic_realm_checkout_"+owner, time.Now()); e != nil {
					t.Fatal(e)
				}
				query(t, f.db, `UPDATE owned_realms SET account_id=$2 WHERE id=$1`, realm, owner)
				if state == "changed_owner_disabled" {
					query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, owner)
				}
			}
			paid, e := f.r.PayOrder(f.buyerCtx, f.buyer, f.order.ID, &f.wallet.ID)
			if state != "healthy" && state != "changed_owner_healthy" {
				if !errors.Is(e, c.ErrForbidden) {
					t.Fatal("ineligible realm owner charged", e)
				}
				assertCheckoutOwnerRollback(t, f)
				return
			}
			if e != nil || paid.Status != c.Paid || paid.PayeeWalletID != f.payee.ID {
				t.Fatal("realm ownership changed captured beneficiary", paid, e)
			}
			walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "95.753", "4.247", "95.753")
		})
	}
}
