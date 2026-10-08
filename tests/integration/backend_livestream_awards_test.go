// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"testing"
	"time"
)

func TestPGBackendLivestreamAwardsComposition(t *testing.T) {
	f := newBackendFixture(t)
	ctx := context.Background()
	treasuryActor := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(ctx, f.db, treasuryActor, "synthetic_award_treasury_"+strings.ReplaceAll(treasuryActor, "-", ""), f.config.Now()); e != nil {
		t.Fatal(e)
	}
	wallets := walletpg.New(f.db)
	beneficiary := walletCreate(t, wallets, f.actor)
	donor := walletCreate(t, wallets, f.other)
	treasury := walletCreate(t, wallets, treasuryActor)
	currency := "synthetic-composed-award"
	walletFund(t, f.db, donor, currency, "100.000")
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1::uuid,false,$2::timestamptz)`, f.other, f.config.Now())
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	binding := livestreampg.AwardMaintenanceConfig{PointsCurrency: currency, TreasuryAccountID: treasuryActor, TreasuryWalletID: treasury.ID}
	cfg := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Livestreams: &application.LivestreamConfig{Provider: &streamProvider{endpoint: "wss://synthetic-awards-" + f.actor + ".invalid"}, Policy: streamConfig(), Awards: &livestreampg.AwardConfig{PointsCurrency: currency, TreasuryAccountID: treasuryActor, TreasuryWalletID: treasury.ID, MaxAmount: walletAmount(t, "100"), MaxOrdersPerAccountStream: 10, MaxAwardsPerStream: 100, OrderTTL: time.Hour}}}
	app, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1::uuid,2,$2::timestamptz)`, f.actor, f.config.Now())
	pub := backendDecode[accounts.Publisher](t, backendJSONCall(t, app, "POST", "/sphere/publishers/individual", f.token, map[string]string{"name": "liveaward-" + f.actor, "nick": "Synthetic award"}, 200))
	stream := backendDecode[live.Stream](t, backendJSONCall(t, app, "POST", "/sphere/livestreams?pub="+pub.Name, f.token, map[string]string{"title": "Synthetic award stream"}, 200))
	path := "/api/livestreams/" + stream.ID
	backendJSONCall(t, app, "POST", path+"/start", f.token, map[string]bool{"no_ingress": true}, 200)
	order := backendDecode[live.AwardOrderResponse](t, backendJSONCall(t, app, "POST", path+"/awards", f.otherToken, map[string]any{"amount": 10}, 200))
	if got := backendDecode[[]live.Award](t, backendCall(t, app, "GET", path+"/awards", "", "", 200)); len(got) != 0 {
		t.Fatal("unpaid award visible", got)
	}
	paid := backendDecode[commerce.Order](t, backendJSONCall(t, app, "POST", "/wallet/orders/"+order.OrderID+"/pay", f.otherToken, map[string]any{"payer_wallet_id": donor.ID}, 200))
	if paid.Status != commerce.Finished {
		t.Fatal(paid)
	}
	got := backendDecode[[]live.Award](t, backendCall(t, app, "GET", path+"/awards", "", "", 200))
	if len(got) != 1 || got[0].AccountID != f.other || got[0].Amount.String() != "10.000" {
		t.Fatal(got)
	}
	walletAssertBalance(t, wallets, donor.ID, currency, "90.000", "0.000", "90.000")
	walletAssertBalance(t, wallets, treasury.ID, currency, "1.000", "9.000", "1.000")
	backendCall(t, app, "POST", path+"/end", f.token, "", 200)
	cfg.Livestreams = nil
	cfg.LivestreamAwardMaintenance = &binding
	maintenance, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = maintenance.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	backendCall(t, maintenance, "GET", path, "", "", 503)
	for _, want := range []int{1, 0} {
		n, e := maintenance.ProcessExternalDue(ctx, 100)
		if e != nil || n != want {
			t.Fatal("maintenance count", n, want, e)
		}
	}
	walletAssertBalance(t, wallets, beneficiary.ID, currency, "9.000", "0.000", "9.000")
	walletAssertBalance(t, wallets, treasury.ID, currency, "1.000", "0.000", "1.000")
}
