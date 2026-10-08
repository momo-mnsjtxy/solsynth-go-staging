// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"fmt"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/ports/authority"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
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
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,2,now())`, f.actor)
	pub := backendDecode[profiles.Publisher](t, backendCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Progression source"}`, "progress_"+strings.ReplaceAll(f.actor, "-", "")), 200))
	backendCall(t, f.app, "POST", "/api/posts?pub="+pub.Name, f.token, `{"content":"Canonical progression source","visibility":0}`, 200)
	if _, e := f.app.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal("canonical scheduled work", e)
	}
	var first int
	if e := f.db.QueryRow(`SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND source_key LIKE 'progression:%'`, f.actor).Scan(&first); e != nil || first < 1 {
		t.Fatal("progression inbox missing", first, e)
	}
	if _, e := f.app.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	var second int
	if e := f.db.QueryRow(`SELECT count(*) FROM notification_v1.outbox WHERE account_id=$1 AND source_key LIKE 'progression:%'`, f.actor).Scan(&second); e != nil || second != first {
		t.Fatal("progression duplicate inbox", first, second, e)
	}
	if _, e := notificationpg.New(f.db, nil, "dev.solsynth.solian").ProcessPending(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	inbox := backendCall(t, f.app, "GET", "/api/notifications", f.token, "", 200)
	if !strings.Contains(inbox.Body.String(), "progression") {
		t.Fatal("progression not materialized into inbox", inbox.Body.String())
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
