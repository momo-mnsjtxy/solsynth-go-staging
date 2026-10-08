// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/wallet/merchants"
	"testing"
)

func TestPGBackendMerchantCatalogComposition(t *testing.T) {
	f := newCommerceFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	app := backendApp(t, f.db, f.config)
	root := "/wallet/merchants/" + f.publisher
	profile := backendDecode[merchants.Profile](t, backendCall(t, app, "GET", root, f.token, "", 200))
	if profile.PublisherID != f.publisher {
		t.Fatal("wrong canonical publisher")
	}
	backendCall(t, app, "PATCH", root+"/apps/"+f.app+"/wallet", f.token, fmt.Sprintf(`{"wallet_id":%q}`, f.payee.ID), 200)
	v := merchantDefinition(f, "composed-"+f.app)
	v.AppIdentifier = &f.app
	body, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "POST", "/wallet/admin/subscriptions/catalog", f.token, string(body), 201)
	backendCall(t, app, "GET", "/api/admin/subscriptions/catalog/"+v.Identifier, f.token, "", 200)
	backendCall(t, app, "GET", root+"/orders/report", f.token, "", 200)
	backendCall(t, app, "DELETE", "/api/admin/subscriptions/catalog/"+v.Identifier, f.token, "", 204)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, nil, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "POST", "/api/admin/subscriptions/catalog", f.token, string(body), 403)
}
