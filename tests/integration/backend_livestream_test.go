// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"testing"
)

func TestPGBackendLivestreamComposition(t *testing.T) {
	f := newBackendFixture(t)
	provider := &streamProvider{endpoint: "wss://synthetic-composed-" + f.actor + ".invalid"}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Livestreams: &application.LivestreamConfig{Provider: provider, Policy: streamConfig()}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1::uuid,2,$2::timestamptz)`, f.actor, f.config.Now())
	pub := backendDecode[accounts.Publisher](t, backendJSONCall(t, app, "POST", "/sphere/publishers/individual", f.token, map[string]string{"name": "live-" + f.actor, "nick": "Synthetic composed livestream"}, 200))
	stream := backendDecode[live.Stream](t, backendJSONCall(t, app, "POST", "/sphere/livestreams?pub="+pub.Name, f.token, map[string]any{"title": "Synthetic broadcast"}, 200))
	backendCall(t, app, "GET", "/api/livestreams/"+stream.ID+"/token", f.otherToken, "", 409)
	backendJSONCall(t, app, "POST", "/sphere/livestreams/"+stream.ID+"/start", f.token, map[string]any{"no_ingress": true}, 200)
	viewer := backendDecode[live.TokenResponse](t, backendCall(t, app, "GET", "/api/livestreams/"+stream.ID+"/token", f.otherToken, "", 200))
	if viewer.IsStreamer || viewer.Token == "" {
		t.Fatal("viewer capability")
	}
	backendCall(t, app, "GET", "/api/livestreams/"+stream.ID+"/token?streamer=true", f.otherToken, "", 403)
	host := backendDecode[live.TokenResponse](t, backendCall(t, app, "GET", "/sphere/livestreams/"+stream.ID+"/token?streamer=true", f.token, "", 200))
	if !host.IsStreamer {
		t.Fatal("owner publishing unavailable")
	}
	backendJSONCall(t, app, "PATCH", "/api/livestreams/"+stream.ID, f.token, map[string]any{"visibility": live.Private}, 409)
	backendCall(t, app, "POST", "/api/livestreams/"+stream.ID+"/end", f.token, "", 200)
	query(t, f.db, `UPDATE owned_livestream_runs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1::uuid`, stream.ID)
	if _, e = app.ProcessExternalDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	backendJSONCall(t, app, "PATCH", "/api/livestreams/"+stream.ID, f.token, map[string]any{"visibility": live.Private}, 200)
	backendCall(t, app, "GET", "/api/livestreams/"+stream.ID, f.otherToken, "", 404)
	var phase string
	if e = f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1::uuid`, stream.ID).Scan(&phase); e != nil || phase != "done" {
		t.Fatal("durable cleanup", phase, e)
	}
}
