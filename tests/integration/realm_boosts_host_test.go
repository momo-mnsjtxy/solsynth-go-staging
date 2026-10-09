// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"crypto/sha256"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
)

func TestPGBackendRealmBoostRoutes(t *testing.T) {
	f := newRealmBoostFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	pepper := sha256.Sum256([]byte("synthetic realm boost host PIN"))
	peppers := map[string][]byte{"synthetic": pepper[:]}
	cfg := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, RealmBoosts: &f.cfg, PINPeppers: peppers}
	app, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	base := "/passport/realms/" + f.realm.Slug + "/boosts"
	backendCall(t, app, "POST", base, "", `{"shares":1}`, 401)
	w := backendCall(t, app, "POST", base, f.token, `{"shares":1,"currency":"golds"}`, 200)
	order := backendDecode[b.OrderResponse](t, w)
	payPath := "/wallet/orders/" + order.OrderID + "/pay"
	payment := map[string]any{"payer_wallet_id": f.wallet.ID}
	backendJSONCall(t, app, "POST", payPath, f.token, payment, 503)
	pin := walletpg.NewPINVerifier(f.db, peppers).WithAuthority(f.guard).WithClock(f.config.Now)
	if e = pin.ProvisionPIN(f.ctx, f.actor, syntheticPaymentPIN, "synthetic"); e != nil {
		t.Fatal(e)
	}
	backendJSONCall(t, app, "POST", payPath, f.token, payment, 403)
	payment["pin_code"] = "000000"
	backendJSONCall(t, app, "POST", payPath, f.token, payment, 403)
	limited, e := f.issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "Synthetic pay-only scope", Scopes: []string{"orders.pay"}})
	if e != nil {
		t.Fatal(e)
	}
	payment["pin_code"] = syntheticPaymentPIN
	backendJSONCall(t, app, "POST", payPath, limited.Token, payment, 403)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_realm_boost_experience WHERE order_id=$1::uuid`, order.OrderID); n != 0 {
		t.Fatal("unverified or scope-limited checkout awarded XP", n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "golds", "100.000", "0.000", "100.000")
	paid := backendDecode[c.Order](t, backendJSONCall(t, app, "POST", payPath, f.token, payment, 200))
	if paid.Status != c.Finished || paid.TransactionID == nil {
		t.Fatal(paid)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.actor); n != 1 {
		t.Fatal("payment proof was not consumed with settlement", n)
	}
	w = backendCall(t, app, "GET", base, "", "", 200)
	status := backendDecode[b.Status](t, w)
	if status.BoostPoints != 1 {
		t.Fatal(status)
	}
	backendCall(t, app, "GET", base+"/leaderboard", "", "", 200)
	w = backendCall(t, app, "GET", "/api/realms/"+f.realm.ID+"/members/"+f.actor+"/experience", "", "", 200)
	if w.Header().Get("X-Realm-Experience-Scope") != "realm.boost.purchase" || w.Header().Get("X-Realm-Experience-Complete") != "false" {
		t.Fatal("boost-only history lacks scope disclosure", w.Header())
	}
	cfg.RealmBoosts = nil
	disabled, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, disabled, "GET", base, "", "", 503)
}
