// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/callpg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/messager/rooms"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
	"time"
)

func TestPGBackendCallsFederationComposition(t *testing.T) {
	f := newBackendFixture(t)
	ctx := context.Background()
	query(t, f.db, callpg.Schema)
	query(t, f.db, federationpg.Schema)
	// Disabled optional modules remain explicit without contacting a provider.
	backendCall(t, f.app, "GET", "/activitypub/actors/unknown", "", "", 503)
	backendCall(t, f.app, "GET", "/messager/chat/realtime/"+authorityUUID(t), f.token, "", 503)
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,1,now())`, f.actor)
	name := "synthetic_external_" + strings.ReplaceAll(f.actor, "-", "")
	pub := backendDecode[profiles.Publisher](t, backendCall(t, f.app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Synthetic external publisher"}`, name), 200))
	local, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	remote, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	keys := federationKeys{locals: map[string]*rsa.PrivateKey{pub.ID: local}, remote: federation.RemoteKey{Actor: "https://remote.example.test/actors/" + name, Inbox: "https://remote.example.test/inbox", KeyID: "https://remote.example.test/actors/" + name + "#key", PublicKey: &remote.PublicKey}}
	fedPolicy := federation.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, MaxAttempts: 3, DeliveryTimeout: time.Second}
	provider := &callProvider{}
	transport := &federationCapture{}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	config := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Calls: &application.CallConfig{Provider: provider, TokenTTL: time.Minute, CallTTL: time.Hour}, Federation: &application.FederationConfig{Policy: fedPolicy, Keys: keys, Transport: transport}}
	app, e := application.NewPostgres(f.db, nil, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		query(t, f.db, `UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1`, pub.ID)
		query(t, f.db, `UPDATE owned_federation_deliveries SET status='cancelled',lease_token=NULL,lease_until=NULL WHERE publisher_id=$1 AND status IN('pending','inflight')`, pub.ID)
	})
	backendCall(t, app, "PATCH", "/api/activitypub/publishers/"+pub.ID+"/settings", f.otherToken, `{"enabled":true,"manually_approves_followers":false}`, 403)
	backendCall(t, app, "PATCH", "/api/activitypub/publishers/"+pub.ID+"/settings", f.token, `{"enabled":true,"manually_approves_followers":false}`, 204)
	actorURL := fedPolicy.Origin + "/activitypub/actors/" + name
	actor := backendDecode[federation.Actor](t, backendCall(t, app, "GET", actorURL, "", "", 200))
	if actor.ID != actorURL || actor.PublicKey.Owner != actorURL {
		t.Fatal("canonical public routing", actor)
	}
	backendCall(t, app, "GET", fedPolicy.Origin+"/.well-known/webfinger?resource=acct:"+name+"@local.example.test", "", "", 200)
	object, _ := json.Marshal(actorURL)
	follow := federation.Activity{Context: federation.Context, ID: keys.remote.Actor + "/follow/" + authorityUUID(t), Type: "Follow", Actor: keys.remote.Actor, Object: object, To: []string{actorURL}}
	raw, _ := json.Marshal(follow)
	req := httptest.NewRequest("POST", actorURL+"/inbox", bytes.NewReader(raw))
	req.Header.Set("Content-Type", federation.MediaType)
	if e = federation.Sign(req, raw, keys.remote.KeyID, remote, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatal("signed canonical request altered", w.Code, w.Body.String())
	}
	post := backendDecode[posts.Post](t, backendCall(t, app, "POST", "/sphere/posts?pub="+pub.Name, f.token, `{"content":"Synthetic public federation post","visibility":0}`, 200))
	room := backendDecode[rooms.Room](t, backendCall(t, app, "POST", "/messager/chat", f.token, `{"name":"Synthetic composed call","is_public":true,"is_community":true}`, 200))
	backendCall(t, app, "POST", "/messager/chat/"+room.ID+"/members/me", f.otherToken, `{}`, 200)
	call := backendDecode[calls.Call](t, backendCall(t, app, "POST", "/messager/chat/realtime/"+room.ID, f.token, `{}`, 200))
	join := backendDecode[calls.JoinResponse](t, backendCall(t, app, "GET", "/api/chat/realtime/"+room.ID+"/join", f.otherToken, "", 200))
	if join.CallID != call.ID || join.Token == "" || join.IsAdmin {
		t.Fatal(join)
	}
	if _, e = app.ProcessDue(ctx, 100); e != nil {
		t.Fatal(e)
	}
	if len(transport.requests) < 2 {
		t.Fatal("host did not send Accept and Create", len(transport.requests))
	}
	var projected bool
	for _, b := range transport.requests {
		var a federation.Activity
		if json.Unmarshal(b, &a) != nil {
			t.Fatal("bad delivery")
		}
		if a.Type == "Create" && bytes.Contains(a.Object, []byte(post.ID)) {
			projected = true
		}
	}
	if !projected {
		t.Fatal("canonical post not federated")
	}
	var replay int
	if e = f.db.QueryRow(`SELECT count(*) FROM notification_v1.events WHERE audience_id=$1 AND type='owned.call.started'`, room.ID).Scan(&replay); e != nil || replay == 0 {
		t.Fatal("call stream projection", replay, e)
	}
	if e = f.issuer.Revoke(ctx, f.otherToken, "current"); e != nil {
		t.Fatal(e)
	}
	if _, e = app.ProcessDue(ctx, 100); e != nil {
		t.Fatal(e)
	}
	var left int
	if e = f.db.QueryRow(`SELECT count(*) FROM call_v1.members WHERE call_id=$1 AND state='left'`, call.ID).Scan(&left); e != nil || left != 1 {
		t.Fatal("host revocation maintenance", left, e)
	}
	backendCall(t, app, "POST", "/api/chat/realtime/"+room.ID+"/end", f.token, `{}`, 204)
	if _, e = application.NewPostgres(f.db, database(t), config); e == nil {
		t.Fatal("external module accepted separate authority pool")
	}
}
func TestPGBackendCallsFederationRestartPersistence(t *testing.T) {
	db := database(t)
	query(t, db, callpg.Schema)
	query(t, db, federationpg.Schema)
	const actor = "ca110000-0000-4000-8000-000000000001"
	const room = "ca110000-0000-4000-8000-000000000002"
	const member = "ca110000-0000-4000-8000-000000000003"
	const call = "ca110000-0000-4000-8000-000000000004"
	const pub = "ca110000-0000-4000-8000-000000000005"
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		query(t, db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at)VALUES($1,'synthetic_external_restart','Synthetic restart',now())`, actor)
		query(t, db, `INSERT INTO chat_rooms(id,is_public,encryption_mode,owned_owner_id,owned_name)VALUES($1,false,77,$2,'Synthetic restart')`, room, actor)
		query(t, db, `INSERT INTO chat_members(id,chat_room_id,account_id,joined_at)VALUES($1,$2,$3,now())`, member, room, actor)
		query(t, db, `INSERT INTO call_v1.calls(id,room_id,sender_id,provider,provider_endpoint,session_id,state,created_at,updated_at,expires_at,ended_at)VALUES($1,$2,$3,'synthetic','wss://synthetic.invalid','synthetic_restart','ended',now(),now(),now(),now())`, call, room, member)
		query(t, db, `INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at)VALUES($1,'synthetic_external_restart','Synthetic restart',0,$2,now(),now())`, pub, actor)
		query(t, db, `INSERT INTO owned_federation_publishers(publisher_id,enabled,manual,updated_at)VALUES($1,false,true,now())`, pub)
	case "after":
	default:
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	var ended, disabled bool
	if e := db.QueryRow(`SELECT (SELECT state='ended' AND ended_at IS NOT NULL FROM call_v1.calls WHERE id=$1),(SELECT NOT enabled FROM owned_federation_publishers WHERE publisher_id=$2)`, call, pub).Scan(&ended, &disabled); e != nil || !ended || !disabled {
		t.Fatal("external state did not survive restart", ended, disabled, e)
	}
}
