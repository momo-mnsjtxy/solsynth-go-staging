// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/namecardpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/passport/rewind"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strconv"
	"testing"
)

func TestPGBackendPaidNameCardComposition(t *testing.T) {
	f := newBackendFixture(t)
	wr := walletpg.New(f.db)
	payer, payee := walletCreate(t, wr, f.actor), walletCreate(t, wr, f.other)
	walletFund(t, f.db, payer, "synthetic-name-points", "100")
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1::uuid,false,now())`, f.actor)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	options := namecardpg.Options{Enabled: true, PricePoints: walletAmount(t, "10"), PointsCurrency: "synthetic-name-points", PayeeWalletID: payee.ID, PurchasePeriodDays: 30, MaxPurchases: 1}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, NameCards: &options})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	order := backendDecode[namecards.OrderResponse](t, backendCall(t, app, "POST", "/passport/accounts/me/name-change-card/order", f.token, "", 200))
	backendJSONCall(t, app, "POST", "/api/accounts/me/name-change-card/use", f.token, namecards.UseRequest{Target: "account", NewName: "renamed-" + f.actor}, 400)
	paid := backendDecode[commerce.Order](t, backendJSONCall(t, app, "POST", "/wallet/orders/"+order.OrderID+"/pay", f.token, map[string]any{"payer_wallet_id": payer.ID}, 200))
	if paid.Status != commerce.Finished {
		t.Fatal("order unsettled")
	}
	cards := backendDecode[[]namecards.Purchase](t, backendCall(t, app, "GET", "/stargate/accounts/me/name-change-card", f.token, "", 200))
	if len(cards) != 1 || cards[0].FulfilledAt == nil {
		t.Fatal("missing settlement receipt")
	}
	backendJSONCall(t, app, "POST", "/stargate/accounts/me/name-change-card/use", f.token, namecards.UseRequest{Target: "account", NewName: "renamed-" + f.actor}, 200)
	backendCall(t, app, "GET", "/stargate/accounts/me", f.token, "", 401)
	newToken := pgIssue(t, f.issuer, f.actor).Token
	own := backendDecode[accounts.Account](t, backendCall(t, app, "GET", "/api/accounts/me", newToken, "", 200))
	if own.Name != "renamed-"+f.actor {
		t.Fatal("rename not projected")
	}
	backendJSONCall(t, app, "POST", "/api/accounts/me/name-change-card/use", newToken, namecards.UseRequest{Target: "account", NewName: "again-" + f.actor}, 400)
	walletAssertBalance(t, wr, payer.ID, "synthetic-name-points", "90.000", "0.000", "90.000")
}
func TestPGBackendRewindComposition(t *testing.T) {
	f := newBackendFixture(t)
	point := backendDecode[rewind.Point](t, backendCall(t, f.app, "GET", "/passport/rewind/me", f.token, "", 200))
	if point.AccountID != f.actor || point.Projection != "owned-partial-v1" || len(point.UnavailableComponents) != 2 {
		t.Fatal("report identity or unavailable providers")
	}
	path := "/stargate/rewind/me/" + strconv.Itoa(point.Year)
	shared := backendDecode[rewind.Point](t, backendCall(t, f.app, "POST", path+"/public", f.token, "", 200))
	if shared.SharableCode == nil {
		t.Fatal("missing share code")
	}
	backendCall(t, f.app, "GET", "/api/rewind/"+*shared.SharableCode, "", "", 200)
	backendCall(t, f.app, "POST", path+"/private", f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/rewind/"+*shared.SharableCode, "", "", 404)
}
