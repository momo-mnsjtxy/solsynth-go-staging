// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
	"testing"
)

func TestPGBackendPresenceProgressionLocationComposition(t *testing.T) {
	f := newBackendFixture(t)
	backendCall(t, f.app, "POST", "/passport/accounts/me/presence/heartbeat", f.token, `{"is_idle":false}`, 200)
	backendCall(t, f.app, "GET", "/api/accounts/me/statuses", f.token, "", 200)
	backendCall(t, f.app, "POST", "/passport/accounts/me/check-in", f.token, "null", 200)
	backendCall(t, f.app, "POST", "/api/accounts/me/check-in", f.token, "null", 200)
	var xp int64
	if e := f.db.QueryRow(`SELECT coalesce(sum(delta),0) FROM owned_progression_experience WHERE account_id=$1`, f.actor).Scan(&xp); e != nil || xp != 100 {
		t.Fatal("check-in reward replay", xp, e)
	}
	meet := backendDecode[locations.Meet](t, backendCall(t, f.app, "POST", "/passport/meets", f.token, `{"location_wkt":"POINT(10 20)","visibility":1}`, 200))
	if meet.ID == "" {
		t.Fatal("meet identity missing")
	}
	backendCall(t, f.app, "GET", "/api/meets/"+meet.ID, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/passport/meets/"+meet.ID, f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/accounts/me/progression/achievements", f.token, "", 200)
	if _, e := f.app.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal("canonical scheduled work", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{}, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", "/api/accounts/me/check-in", f.token, "null", 200)
	backendCall(t, f.app, "POST", "/api/meets", f.token, `{}`, 403)
	if e := f.issuer.Revoke(context.Background(), f.token, "current"); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", "/api/accounts/me/check-in", f.token, "null", 401)
}
