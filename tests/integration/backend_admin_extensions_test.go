// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"encoding/json"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/automod"
	"testing"
)

func TestPGBackendAdminExtensionsComposition(t *testing.T) {
	f := newAdminExtensionsFixture(t)
	app := backendApp(t, f.db, f.config)
	for _, tc := range []struct{ service, field string }{{"passport", "total_profiled_accounts"}, {"sphere", "total_posts"}, {"wallet", "total_wallets"}} {
		backendCall(t, app, "GET", "/"+tc.service+"/admin/stats", f.token, "", 403)
		for _, path := range []string{"/" + tc.service + "/admin/stats", "/api/admin/" + tc.service + "/stats"} {
			body := backendDecode[map[string]json.RawMessage](t, backendCall(t, app, "GET", path, f.outsideToken, "", 200))
			if _, ok := body[tc.field]; !ok {
				t.Fatal("wrong service statistics", path)
			}
		}
	}
	backendCall(t, app, "GET", "/api/admin/stats", f.outsideToken, "", 404)
	route := "/passport/admin/accounts/" + f.actor + "/verification"
	backendJSONCall(t, app, "POST", route, f.token, syntheticMark(), 403)
	backendJSONCall(t, app, "POST", route, f.outsideToken, syntheticMark(), 200)
	account := backendDecode[accounts.Account](t, backendCall(t, app, "GET", "/api/accounts/"+f.actor, "", "", 200))
	if account.Profile == nil || account.Profile.Verification == nil || account.Profile.Verification.Type != 0 {
		t.Fatal("canonical mark absent")
	}
	backendCall(t, app, "DELETE", route, f.outsideToken, "", 204)
	account = backendDecode[accounts.Account](t, backendCall(t, app, "GET", "/api/accounts/"+f.actor, "", "", 200))
	if account.Profile == nil || account.Profile.Verification != nil {
		t.Fatal("mark not cleared")
	}
	rule := backendDecode[automod.Rule](t, backendJSONCall(t, app, "POST", "/sphere/automod/rules", f.outsideToken, rulePatch("composed-"+f.actor), 201))
	result := backendDecode[[]automod.Result](t, backendJSONCall(t, app, "POST", "/api/automod/rules/"+rule.ID+"/test", f.outsideToken, map[string]string{"content": "a synthetic marker appears"}, 200))
	if len(result) != 1 || result[0].RuleID != rule.ID || result[0].Action != automod.Flag {
		t.Fatal("explicit evaluation result")
	}
	backendCall(t, app, "DELETE", "/api/automod/rules/"+rule.ID, f.outsideToken, "", 204)
	backendCall(t, app, "GET", "/sphere/automod/rules/"+rule.ID, f.outsideToken, "", 404)
}
