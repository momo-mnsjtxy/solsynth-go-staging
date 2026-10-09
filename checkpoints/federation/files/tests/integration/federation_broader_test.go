// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/socialpg"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func acceptRemoteForBroaderTest(t *testing.T, x *federationFixture) {
	t.Helper()
	if err := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: x.keys.remote.Actor}); err != nil {
		t.Fatal(err)
	}
	query(t, x.db, `UPDATE owned_federation_relationships SET status='accepted' WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, x.publisher, x.keys.remote.Actor)
}

func TestPGFederationPrivateMediaModerationAndFeatured(t *testing.T) {
	x := newFederationFixture(t, true)
	acceptRemoteForBroaderTest(t, x)
	private := f.Note{ID: x.keys.remote.Actor + "/notes/private", Type: "Note", AttributedTo: x.keys.remote.Actor, Content: "direct", To: []string{x.actorURI}, Attachments: []f.Attachment{{Type: "Image", MediaType: "image/png", URL: "https://remote.example.test/media/1"}}}
	create := x.activity(t, "Create", private)
	create.To = []string{x.actorURI}
	if err := x.receive(t, create); err != nil {
		t.Fatal("direct private activity rejected", err)
	}
	var retained f.Note
	var raw []byte
	if err := x.db.QueryRow(`SELECT object FROM owned_federation_objects WHERE publisher_id=$1 AND object_id=$2`, x.publisher, private.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &retained) != nil || len(retained.Attachments) != 1 || retained.Attachments[0].URL != private.Attachments[0].URL {
		t.Fatal("bounded media projection missing", string(raw))
	}
	bad := private
	bad.ID = x.keys.remote.Actor + "/notes/cross-origin"
	bad.Attachments = []f.Attachment{{Type: "Image", URL: "https://other.example.test/private"}}
	activity := x.activity(t, "Create", bad)
	activity.To = []string{x.actorURI}
	if err := x.receive(t, activity); !errors.Is(err, f.ErrForbidden) {
		t.Fatal("cross-origin media admitted", err)
	}
	rule := authorityUUID(t)
	query(t, x.db, `INSERT INTO owned_federation_moderation_rules(id,name,kind,action,pattern,enabled,priority,created_at,updated_at)VALUES($1,'suspend actor','actor','suspend',$2,true,0,now(),now())`, rule, x.keys.remote.Actor)
	blocked := private
	blocked.ID = x.keys.remote.Actor + "/notes/blocked"
	activity = x.activity(t, "Create", blocked)
	activity.To = []string{x.actorURI}
	if err := x.receive(t, activity); !errors.Is(err, f.ErrForbidden) {
		t.Fatal("moderation not enforced before write", err)
	}
	post := x.makePost(t, "featured", posts.Public, false)
	if err := x.r.SetFeatured(x.ctx, x.actor, x.publisher, []string{post.ID}); err != nil {
		t.Fatal(err)
	}
	collection, err := x.r.Featured(x.ctx, x.name, 0, 20)
	if err != nil || collection.Total != 1 || len(collection.Items) != 1 {
		t.Fatal(collection, err)
	}
}

func TestPGFederationMoveRebindsRelationshipsToPinnedTarget(t *testing.T) {
	x := newFederationFixture(t, true)
	acceptRemoteForBroaderTest(t, x)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	next := f.RemoteKey{Actor: "https://remote.example.test/actors/moved", Inbox: "https://remote.example.test/inbox/moved", KeyID: "https://remote.example.test/actors/moved#main-key", PublicKey: &key.PublicKey}
	keys := federationPoolKeys{federationKeys: x.keys, second: next}
	repo, err := federationpg.New(x.db, x.config, keys, x.guard)
	if err != nil {
		t.Fatal(err)
	}
	move := x.activity(t, "Move", x.keys.remote.Actor)
	move.Target = json.RawMessage(`"` + next.Actor + `"`)
	if err = repo.Receive(x.ctx, x.name, x.verified(t, move)); err != nil {
		t.Fatal(err)
	}
	var oldCount, newCount int
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2`, x.publisher, x.keys.remote.Actor).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2`, x.publisher, next.Actor).Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || newCount != 1 {
		t.Fatal("move did not atomically rebind", oldCount, newCount)
	}
}

func TestPGFederationRealmFollowAndSignedDelivery(t *testing.T) {
	x := newFederationFixture(t, true)
	query(t, x.db, socialpg.Schema)
	realmID, orgID := authorityUUID(t), authorityUUID(t)
	slug := "community_" + x.name
	query(t, x.db, `INSERT INTO owned_realms(id,slug,name,description,account_id,is_public,is_community,created_at,updated_at)VALUES($1,$2,'Community','Realm',$3,true,true,now(),now())`, realmID, slug, x.actor)
	query(t, x.db, `INSERT INTO owned_realm_members(id,realm_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,100,now(),now(),now())`, authorityUUID(t), realmID, x.actor)
	query(t, x.db, `INSERT INTO owned_publishers(id,name,nick,bio,type,realm_id,created_at,updated_at)VALUES($1,$2,$2,'',1,$3,now(),now())`, orgID, "org_"+x.name, realmID)
	query(t, x.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,200,now(),now(),now())`, authorityUUID(t), orgID, x.actor)
	local, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	x.keys.locals[orgID] = local
	actorURI := x.config.Origin + "/activitypub/realms/" + slug
	query(t, x.db, `INSERT INTO owned_federation_realms(realm_id,enabled,actor_uri,key_id,updated_at)VALUES($1,true,$2,$2||'#main-key',now())`, realmID, actorURI)
	actor, err := x.r.RealmActor(x.ctx, slug)
	if err != nil || actor.Type != "Group" || actor.ID != actorURI {
		t.Fatal(actor, err)
	}
	follow := x.activity(t, "Follow", actorURI)
	body, _ := json.Marshal(follow)
	request := httptest.NewRequest("POST", actorURI+"/inbox", bytes.NewReader(body))
	if err = f.Sign(request, body, x.keys.remote.KeyID, x.remoteKey, x.config.Clock()); err != nil {
		t.Fatal(err)
	}
	verified, err := f.Verify(x.ctx, x.config, x.keys, request, body)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.r.ReceiveRealm(x.ctx, slug, verified); err != nil {
		t.Fatal(err)
	}
	capture := &federationCapture{}
	if n, err := x.r.ProcessRealmDeliveries(x.ctx, capture, 10); err != nil || n != 1 || len(capture.requests) != 1 {
		t.Fatal(n, err, len(capture.requests))
	}
	note := f.Note{ID: x.keys.remote.Actor + "/notes/realm", Type: "Note", AttributedTo: x.keys.remote.Actor, Content: "realm post", To: []string{f.Public}}
	create := x.activity(t, "Create", note)
	create.To = []string{f.Public}
	body, _ = json.Marshal(create)
	request = httptest.NewRequest("POST", actorURI+"/inbox", bytes.NewReader(body))
	if err = f.Sign(request, body, x.keys.remote.KeyID, x.remoteKey, x.config.Clock()); err != nil {
		t.Fatal(err)
	}
	verified, err = f.Verify(x.ctx, x.config, x.keys, request, body)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.r.ReceiveRealm(x.ctx, slug, verified); err != nil {
		t.Fatal(err)
	}
	outbox, err := x.r.RealmCollection(x.ctx, slug, "outbox", 0, 20)
	if err != nil || outbox.Total != 1 || len(outbox.Items) != 1 {
		t.Fatal(outbox, err)
	}
}
