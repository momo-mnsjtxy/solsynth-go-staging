// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	c "solsynth.local/go-migration/ports/admin/collections"
	d "solsynth.local/go-migration/ports/passport/domaintrust"
	"strings"
	"testing"
)

func TestPGBackendDomainCollectionsComposition(t *testing.T) {
	f := newDomainFixture(t)
	app := backendApp(t, f.db, f.config)
	if e := app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"domain_pattern": f.host, "trust_level": d.Blocked, "priority": 100}
	backendJSONCall(t, app, "POST", "/passport/domain-blocks", f.token, body, 403)
	rule := backendDecode[d.Rule](t, backendJSONCall(t, app, "POST", "/passport/domain-blocks", f.outsideToken, body, 200))
	raw := "https://" + f.host + "/synthetic-private-path?synthetic-query=value"
	verdict := backendDecode[d.Result](t, backendJSONCall(t, app, "POST", "/api/domain-blocks/validate", "", map[string]string{"url": raw}, 200))
	if verdict.IsAllowed || verdict.MatchedRule != nil {
		t.Fatal("public verdict leaked rule or allowed blocked host", verdict)
	}
	metric := backendDecode[d.Metric](t, backendCall(t, app, "GET", "/passport/domain-blocks/metrics/"+f.host, f.outsideToken, "", 200))
	if metric.Domain != f.host || metric.CheckCount != 1 || metric.BlockedCount != 1 {
		t.Fatal(metric)
	}
	backendCall(t, app, "GET", "/api/domain-blocks/metrics", f.token, "", 403)
	backendCall(t, app, "DELETE", "/api/domain-blocks/"+rule.ID, f.outsideToken, "", 204)
	id := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_post_collections(id,publisher_id,slug,name,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::text,'Synthetic composed collection',$4::timestamptz,$4::timestamptz)`, id, f.individual.ID, "composed-"+id, f.config.Now())
	path := "/sphere/admin/collections/" + id
	backendCall(t, app, "GET", path, f.token, "", 403)
	got := backendDecode[c.Collection](t, backendJSONCall(t, app, "PATCH", path, f.outsideToken, map[string]string{"name": "Synthetic updated collection"}, 200))
	if got.ID != id || got.Name == nil || *got.Name != "Synthetic updated collection" {
		t.Fatal(got)
	}
	response := backendCall(t, app, "GET", "/api/admin/collections/"+id, f.outsideToken, "", 200)
	if strings.Contains(response.Body.String(), "post_ids") {
		t.Fatal("nested posts exposed")
	}
	backendCall(t, app, "DELETE", path, f.outsideToken, "", 204)
	backendCall(t, app, "GET", path, f.outsideToken, "", 404)
}
