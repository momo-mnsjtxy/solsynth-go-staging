// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/walletpg"
	a "solsynth.local/go-migration/ports/sphere/postawards"
	"solsynth.local/go-migration/ports/sphere/posts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
)

func TestPGBackendPostAwardCheckoutAndRelease(t *testing.T) {
	f := newPostAwardFixture(t, "")
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, PostAwards: &f.cfg})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	p := f.post(t, f.individual, "Synthetic composed paid award", posts.Public)
	path := "/sphere/posts/" + p.ID + "/awards"
	backendJSONCall(t, app, "POST", path, "", map[string]any{"amount": 5}, 401)
	order := backendDecode[a.OrderResponse](t, backendJSONCall(t, app, "POST", path, f.otherToken, map[string]any{"amount": 5.125, "message": "Synthetic client omits attitude"}, 200))
	payPath := "/wallet/orders/" + order.OrderID + "/pay"
	body := map[string]any{"payer_wallet_id": f.payer.ID}
	backendJSONCall(t, app, "POST", payPath, f.otherToken, body, 503)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1::uuid,false,$2::timestamptz)`, f.other, f.config.Now())
	paid := backendDecode[c.Order](t, backendJSONCall(t, app, "POST", payPath, f.otherToken, body, 200))
	if paid.Status != c.Finished {
		t.Fatal(paid)
	}
	backendJSONCall(t, app, "POST", payPath, f.otherToken, body, 200)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.other); n != 1 {
		t.Fatal(n)
	}
	rows := backendDecode[[]a.Award](t, backendCall(t, app, "GET", path, "", "", 200))
	if len(rows) != 1 || rows[0].Attitude != 0 {
		t.Fatal(rows)
	}
	pending := backendDecode[a.Pending](t, backendCall(t, app, "GET", path+"/pending", "", "", 200))
	if pending.PayoutAmount.String() != "5.125" {
		t.Fatal(pending)
	}
	backendCall(t, app, "GET", "/api/merchants/"+f.individual.ID+"/wallet", f.otherToken, "", 403)
	backendCall(t, app, "GET", "/api/merchants/"+f.individual.Name+"/settlements", f.token, "", 200)
	if n, e := app.ProcessPostAwardsDue(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.beneficiary.ID, "points", "5.125", "0.000", "5.125")
}
