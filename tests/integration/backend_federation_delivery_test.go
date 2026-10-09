// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/messagerpg"
	f "solsynth.local/go-migration/ports/federation"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
)

func TestPGBackendFederationDeliveryLeaseComposition(t *testing.T) {
	x := newBackendFixture(t)
	query(t, x.db, federationpg.Schema)
	query(t, x.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,1,now())`, x.actor)
	name := "synthetic_delivery_" + strings.ReplaceAll(x.actor, "-", "")
	pub := backendDecode[profiles.Publisher](t, backendCall(t, x.app, "POST", "/sphere/publishers/individual", x.token, fmt.Sprintf(`{"name":%q,"nick":"Synthetic delivery"}`, name), 200))
	local, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	remote, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	keys := federationKeys{locals: map[string]*rsa.PrivateKey{pub.ID: local}, remote: f.RemoteKey{Actor: "https://remote.example.test/actors/" + name, Inbox: "https://remote.example.test/inbox", KeyID: "https://remote.example.test/actors/" + name + "#key", PublicKey: &remote.PublicKey}}
	var sent atomic.Int64
	transport := deliverySendFunc(func(ctx context.Context, req *http.Request) (int, error) {
		sent.Add(1)
		if e := x.issuer.Revoke(ctx, x.token, "current"); e != nil {
			return 0, e
		}
		return 202, nil
	})
	policy := f.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, MaxAttempts: 3, DeliveryTimeout: 5 * time.Second}
	visibility := int32(0)
	chat := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(x.db, nil, application.Config{Authority: x.config, PublicVisibility: &visibility, MessagerPolicy: &chat, Federation: &application.FederationConfig{Policy: policy, Keys: keys, Transport: transport}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "PATCH", "/api/activitypub/publishers/"+pub.ID+"/settings", x.token, `{"enabled":true,"manually_approves_followers":true}`, 204)
	backendCall(t, app, "POST", "/api/activitypub/publishers/"+pub.ID+"/relationships", x.token, fmt.Sprintf(`{"action":"follow","remote_actor":%q}`, keys.remote.Actor), 204)
	if n, e := app.ProcessExternalDue(context.Background(), 10); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	var state string
	if e = x.db.QueryRow(`SELECT status FROM owned_federation_deliveries WHERE publisher_id=$1`, pub.ID).Scan(&state); e != nil || state != "cancelled" || sent.Load() != 1 {
		t.Fatal("composed late acknowledgement survived", state, sent.Load(), e)
	}
	backendCall(t, app, "GET", "/api/activitypub/publishers/"+pub.ID+"/relationships", x.token, "", 401)
	if n, e := app.ProcessExternalDue(context.Background(), 10); e != nil || n != 0 {
		t.Fatal("terminal request retried", n, e)
	}
}
