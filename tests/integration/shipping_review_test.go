// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
	"time"
)

// Independent shipping review: access to a captured destination depends on the
// current exact client scope and current owner state, including after lock waits.
func TestPGShippingReviewCurrentScopeAndOwnerRevocation(t *testing.T) {
	t.Run("ordinary_contact_scope", func(t *testing.T) {
		f := newShippingFixture(t)
		pair, e := f.issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic scoped contact access", Scopes: []string{"account.contacts.manage"}})
		if e != nil {
			t.Fatal(e)
		}
		got, e := f.contacts.GetContact(f.ctx, pair.Token, f.contact.ID)
		if e != nil || got.Content != f.contact.Content {
			t.Fatal("ordinary explicitly scoped contact session rejected", e)
		}
	})
	for _, boundary := range []string{"app_scope", "owner_disabled"} {
		t.Run(boundary+"_after_wait", func(t *testing.T) {
			f := newShippingFixture(t)
			o := shippingOrder(t, f, "independent-shipping-authority")
			if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
				t.Fatal(e)
			}
			shippingScope(t, f, shippingFullScopes())
			before, e := f.r.OrderFulfillment(f.ctx, o.ID, f.credentials)
			if e != nil || before.AddressSnapshot != f.contact.Content {
				t.Fatal("fixture lacks authorized destination", e)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "owned_custom_apps", f.app
			if boundary == "owner_disabled" {
				table, id = "owned_authority_accounts", f.actor
			}
			blocker, pid := atomicBlocker(t, locks, table, id)
			if boundary == "app_scope" {
				cfg, e := json.Marshal(apps.OAuthConfig{AllowedScopes: []string{"orders.create", "orders.status.manage"}, RedirectURIs: []string{}, AllowedGrantTypes: []string{}})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(ctx, `UPDATE owned_custom_apps SET oauth_config=$2 WHERE id=$1`, f.app, cfg); e != nil {
					t.Fatal(e)
				}
			} else if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor); e != nil {
				t.Fatal(e)
			}
			type result struct {
				value c.MerchantFulfillment
				err   error
			}
			done := make(chan result, 1)
			go func() {
				v, e := f.r.OrderFulfillment(ctx, o.ID, f.credentials)
				done <- result{v, e}
			}()
			waitAtomicBlocked(t, locks, pid, table)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			after := <-done
			if after.err == nil || errors.Is(after.err, context.DeadlineExceeded) || errors.Is(after.err, context.Canceled) || after.value.AddressSnapshot != "" {
				t.Fatal("revoked destination access was not explicitly denied", after.err)
			}
		})
	}
	t.Run("exact_scope_required", func(t *testing.T) {
		f := newShippingFixture(t)
		o := shippingOrder(t, f, "independent-shipping-exact-scope")
		if _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
			t.Fatal(e)
		}
		for _, scope := range []string{"orders.*", "orders.fulfillment.*", "contacts.read", "orders.fulfillment.read.extra"} {
			cfg, _ := json.Marshal(apps.OAuthConfig{AllowedScopes: []string{scope}, RedirectURIs: []string{}, AllowedGrantTypes: []string{}})
			raw, _ := json.Marshal([]string{scope})
			query(t, f.db, `UPDATE owned_custom_apps SET oauth_config=$2 WHERE id=$1`, f.app, cfg)
			query(t, f.db, `UPDATE owned_app_secrets SET scopes=$2 WHERE id=$1`, f.secretID, raw)
			v, e := f.r.OrderFulfillment(f.ctx, o.ID, f.credentials)
			if e == nil || v.AddressSnapshot != "" {
				t.Fatal("non-exact scope disclosed destination", scope, e)
			}
		}
		// An app owner's ordinary bearer token does not replace app credentials.
		shippingScope(t, f, shippingFullScopes())
		credentials := f.credentials
		credentials.ClientSecret = f.token
		if v, e := f.r.OrderFulfillment(f.ctx, o.ID, credentials); e == nil || v.AddressSnapshot != "" {
			t.Fatal("account bearer was treated as an app credential", e)
		}
	})
}
