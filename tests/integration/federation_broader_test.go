// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/socialpg"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type idempotentFederationAdmin struct {
	federationKeys
	mu        sync.Mutex
	receipts  map[string]string
	rotations int
	after     func()
}

func (k *idempotentFederationAdmin) ExecuteLocalKeyCommand(_ context.Context, operation, target, action, expected string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if result := k.receipts[operation]; result != "" {
		return result, nil
	}
	current := k.locals[target]
	if current == nil {
		return "", f.ErrUnavailable
	}
	currentFingerprint, _ := f.KeyFingerprint(&current.PublicKey)
	if currentFingerprint != expected {
		return "", f.ErrForbidden
	}
	if action == "ensure" {
		k.receipts[operation] = currentFingerprint
		return currentFingerprint, nil
	}
	if action != "rotate" {
		return "", f.ErrInvalid
	}
	next, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	result, _ := f.KeyFingerprint(&next.PublicKey)
	k.locals[target] = next
	k.receipts[operation] = result
	k.rotations++
	if k.after != nil {
		after := k.after
		k.after = nil
		after()
	}
	return result, nil
}

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

func TestPGFederationMoveRejectsTargetWithoutReciprocalObservation(t *testing.T) {
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
	if err = repo.Receive(x.ctx, x.name, x.verified(t, move)); !errors.Is(err, f.ErrForbidden) {
		t.Fatal("move without target alsoKnownAs observation was accepted", err)
	}
	var oldCount, newCount int
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2`, x.publisher, x.keys.remote.Actor).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2`, x.publisher, next.Actor).Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 1 || newCount != 0 {
		t.Fatal("rejected move changed relationships", oldCount, newCount)
	}
}

func testDeliveryBinding(t *testing.T, key f.RemoteKey) string {
	t.Helper()
	fingerprint, err := f.KeyFingerprint(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]string{key.Actor, key.Inbox, key.KeyID, fingerprint, key.Incarnation})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestPGFederationMoveBilateralProofAppliesOncePerPublisher(t *testing.T) {
	x := newFederationFixture(t, true)
	old := x.keys.remote
	old.Incarnation = authorityUUID(t)
	nextPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	next := f.RemoteKey{Actor: "https://remote.example.test/actors/moved", Inbox: "https://remote.example.test/inbox/moved", KeyID: "https://remote.example.test/actors/moved#main-key", Incarnation: authorityUUID(t), PublicKey: &nextPrivate.PublicKey}
	base := x.keys
	base.remote = old
	keys := federationPoolKeys{federationKeys: base, second: next}
	repo, err := federationpg.New(x.db, x.config, keys, x.guard)
	if err != nil {
		t.Fatal(err)
	}

	secondName := "move_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:12]
	second, err := accountpg.NewForCompatibilityTests(x.db).CreatePublisher(x.ctx, x.actor, accounts.PublisherPatch{Name: &secondName, Nick: &secondName})
	if err != nil {
		t.Fatal(err)
	}
	local, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys.locals[second.ID] = local
	if err = repo.Settings(x.ctx, x.actor, second.ID, f.Settings{Enabled: true, Manual: true}); err != nil {
		t.Fatal(err)
	}
	for _, publisher := range []string{x.publisher, second.ID} {
		if err = repo.Change(x.ctx, x.actor, publisher, f.Change{Action: "follow", RemoteActor: old.Actor}); err != nil {
			t.Fatal(err)
		}
		query(t, x.db, `UPDATE owned_federation_relationships SET status='accepted' WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, publisher, old.Actor)
		fingerprint, _ := f.KeyFingerprint(next.PublicKey)
		aliases, _ := json.Marshal([]string{old.Actor})
		query(t, x.db, `INSERT INTO owned_federation_actor_assertions(publisher_id,actor_uri,key_id,key_fingerprint,remote_binding,aliases,discovery_id,observed_at)VALUES($1,$2,$3,$4,$5,$6,$7,now())`, publisher, next.Actor, next.KeyID, fingerprint, testDeliveryBinding(t, next), aliases, authorityUUID(t))
	}
	move := x.activity(t, "Move", old.Actor)
	move.Target, _ = json.Marshal(next.Actor)
	verifyFor := func(inbox string) f.Verified {
		body, _ := json.Marshal(move)
		req := httptest.NewRequest("POST", inbox, bytes.NewReader(body))
		if signErr := f.Sign(req, body, old.KeyID, x.remoteKey, x.config.Clock()); signErr != nil {
			t.Fatal(signErr)
		}
		verified, verifyErr := f.Verify(x.ctx, x.config, keys, req, body)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		return verified
	}
	if err = repo.Receive(x.ctx, x.name, verifyFor(x.actorURI+"/inbox")); err != nil {
		t.Fatal(err)
	}
	secondActor := x.config.Origin + "/activitypub/actors/" + secondName
	if err = repo.Receive(x.ctx, secondName, verifyFor(secondActor+"/inbox")); err != nil {
		t.Fatal(err)
	}
	if err = repo.Receive(x.ctx, x.name, verifyFor(x.actorURI+"/inbox")); err != nil {
		t.Fatal(err)
	}
	var applications, oldOut, nextOut int
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_move_applications WHERE old_actor_uri=$1`, old.Actor).Scan(&applications); err != nil {
		t.Fatal(err)
	}
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_relationships WHERE actor_uri=$1 AND direction='out'`, old.Actor).Scan(&oldOut); err != nil {
		t.Fatal(err)
	}
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_relationships WHERE actor_uri=$1 AND direction='out'`, next.Actor).Scan(&nextOut); err != nil {
		t.Fatal(err)
	}
	if applications != 2 || oldOut != 0 || nextOut != 2 {
		t.Fatal("per-publisher Move application", applications, oldOut, nextOut)
	}
}

func TestPGFederationKeyCommandRetriesAuditFailureWithoutSecondRotation(t *testing.T) {
	x := newFederationFixture(t, true)
	admin := &idempotentFederationAdmin{federationKeys: x.keys, receipts: map[string]string{}}
	repo, err := federationpg.New(x.db, x.config, admin, x.guard)
	if err != nil {
		t.Fatal(err)
	}
	operation := authorityUUID(t)
	command, err := repo.SubmitKeyCommand(x.ctx, x.actor, x.publisher, "rotate", operation)
	if err != nil || command.Status != "pending" {
		t.Fatal(command, err)
	}
	query(t, x.db, `CREATE OR REPLACE FUNCTION synthetic_federation_key_audit_failure()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='key.rotate' THEN RAISE EXCEPTION 'synthetic audit failure';END IF;RETURN NEW;END$$`)
	query(t, x.db, `CREATE TRIGGER synthetic_federation_key_audit_failure BEFORE INSERT ON owned_federation_admin_audit FOR EACH ROW EXECUTE FUNCTION synthetic_federation_key_audit_failure()`)
	if _, err = repo.ProcessKeyCommands(x.ctx, 1); err == nil || admin.rotations != 1 {
		t.Fatal("first custody rotation/audit failure was not observed", admin.rotations, err)
	}
	query(t, x.db, `DROP TRIGGER synthetic_federation_key_audit_failure ON owned_federation_admin_audit`)
	query(t, x.db, `DROP FUNCTION synthetic_federation_key_audit_failure()`)
	query(t, x.db, `UPDATE owned_federation_key_commands SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND status='inflight'`, operation)
	if n, processErr := repo.ProcessKeyCommands(x.ctx, 1); processErr != nil || n != 1 || admin.rotations != 1 {
		t.Fatal("idempotent custody retry rotated twice or did not finalize", n, admin.rotations, processErr)
	}
	var status, expected, result, target string
	if err = x.db.QueryRow(`SELECT status,expected_fingerprint,result_fingerprint FROM owned_federation_key_commands WHERE id=$1`, operation).Scan(&status, &expected, &result); err != nil {
		t.Fatal(err)
	}
	if err = x.db.QueryRow(`SELECT target FROM owned_federation_admin_audit WHERE action='key.rotate' ORDER BY id DESC LIMIT 1`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || expected == result || target != x.publisher+":"+expected+"->"+result {
		t.Fatal("key command receipt/audit fingerprints invalid", status, expected, result, target)
	}
}

func TestPGFederationKeyCommandRecordsExternalSuccessDespiteConcurrentRevocation(t *testing.T) {
	x := newFederationFixture(t, true)
	admin := &idempotentFederationAdmin{federationKeys: x.keys, receipts: map[string]string{}}
	repo, err := federationpg.New(x.db, x.config, admin, x.guard)
	if err != nil {
		t.Fatal(err)
	}
	operation := authorityUUID(t)
	if _, err = repo.SubmitKeyCommand(x.ctx, x.actor, x.publisher, "rotate", operation); err != nil {
		t.Fatal(err)
	}
	admin.after = func() { query(t, x.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, x.actor) }
	if n, processErr := repo.ProcessKeyCommands(x.ctx, 1); processErr != nil || n != 1 || admin.rotations != 1 {
		t.Fatal("external success was hidden by revocation", n, admin.rotations, processErr)
	}
	var status, result string
	if err = x.db.QueryRow(`SELECT status,result_fingerprint FROM owned_federation_key_commands WHERE id=$1`, operation).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	var audit int
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_admin_audit WHERE account_id=$1 AND action='key.rotate' AND target LIKE $2`, x.actor, x.publisher+":%->"+result).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || len(result) != 64 || audit != 1 {
		t.Fatal("external result was not reconciled", status, result, audit)
	}
}

func TestPGFederationChangeRechecksOriginalSessionAfterAuditWait(t *testing.T) {
	x := newFederationFixture(t, true)
	query(t, x.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE account_id=$1`, x.actor)
	blocker, err := x.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.Exec(`LOCK TABLE owned_federation_audit IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: x.keys.remote.Actor})
	}()
	deadline := time.Now().Add(time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if scanErr := x.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'INSERT INTO owned_federation_audit%')`).Scan(&blocked); scanErr != nil {
			t.Fatal(scanErr)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("Change never reached the blocked audit boundary")
	}
	time.Sleep(2100 * time.Millisecond)
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if changeErr := <-done; changeErr == nil {
		t.Fatal("expired original session committed Change")
	}
	var relations, audits int
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2`, x.publisher, x.keys.remote.Actor).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if err = x.db.QueryRow(`SELECT count(*) FROM owned_federation_audit WHERE publisher_id=$1 AND action='follow'`, x.publisher).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if relations != 0 || audits != 0 {
		t.Fatal("expired Change did not roll back atomically", relations, audits)
	}
}

func TestPGFederationStaticPinHoldsAdmissionFenceUntilCommit(t *testing.T) {
	x := newFederationFixture(t, true)
	keys := x.keys
	keys.remote.Incarnation = authorityUUID(t)
	repo, err := federationpg.New(x.db, x.config, keys, x.guard)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := x.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.Exec(`LOCK TABLE owned_federation_audit IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	changeDone := make(chan error, 1)
	go func() {
		changeDone <- repo.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: keys.remote.Actor})
	}()
	deadline := time.Now().Add(time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if scanErr := x.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'INSERT INTO owned_federation_audit%')`).Scan(&blocked); scanErr != nil {
			t.Fatal(scanErr)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("static-pin Change never reached commit boundary")
	}
	admission, err := x.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Rollback()
	admissionDone := make(chan error, 1)
	go func() {
		_, lockErr := admission.Exec(`LOCK TABLE owned_federation_public_keys IN ROW EXCLUSIVE MODE`)
		admissionDone <- lockErr
	}()
	select {
	case lockErr := <-admissionDone:
		t.Fatal("durable admission bypassed static absent-row fence", lockErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-changeDone; err != nil {
		t.Fatal(err)
	}
	if err = <-admissionDone; err != nil {
		t.Fatal(err)
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
	followers, err := x.r.RealmCollection(x.ctx, slug, "followers", 0, 20)
	if err != nil || followers.Total != 1 || len(followers.Items) != 0 {
		t.Fatal("public realm followers leaked identities", followers, err)
	}
	capture := &federationCapture{}
	if n, err := x.r.ProcessRealmDeliveries(x.ctx, capture, 10); err != nil || n != 1 || len(capture.requests) != 1 {
		t.Fatal(n, err, len(capture.requests))
	}
	var deliveredID int64
	if err = x.db.QueryRow(`SELECT id FROM owned_federation_realm_deliveries WHERE realm_id=$1 AND activity_type='Accept'`, realmID).Scan(&deliveredID); err != nil {
		t.Fatal(err)
	}
	if _, err = x.db.Exec(`UPDATE owned_federation_realm_deliveries SET status='pending',finished_at=NULL WHERE id=$1`, deliveredID); err == nil {
		t.Fatal("terminal realm delivery was revived")
	}
	if _, err = x.db.Exec(`INSERT INTO owned_federation_realm_deliveries(realm_id,actor_uri,inbox_uri,activity_id,activity_type,payload,remote_binding,local_fingerprint,max_attempts,expires_at,next_attempt,created_at)VALUES($1,$2,$3,'https://local.example.test/bad','Create','{}',$4,$4,1,now()+interval '1 hour',now(),now())`, realmID, x.keys.remote.Actor, x.keys.remote.Inbox, strings.Repeat("a", 64)); err == nil {
		t.Fatal("malformed realm delivery payload was accepted")
	}
	postID := authorityUUID(t)
	now := x.config.Clock().UTC().Truncate(time.Microsecond)
	query(t, x.db, `INSERT INTO owned_text_posts(id,publisher_id,realm_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,$3,'local realm post',0,0,$4,$4,$4)`, postID, orgID, realmID, now)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'create',$3)`, x.actor, postID, now)
	if n, err := x.r.ReconcilePosts(x.ctx, 10); err != nil || n != 1 {
		t.Fatal("realm post was not projected", n, err)
	}
	localCapture := &federationCapture{}
	if n, err := x.r.ProcessRealmDeliveries(x.ctx, localCapture, 10); err != nil || n != 1 || len(localCapture.requests) != 1 {
		t.Fatal("realm post was not delivered", n, err, len(localCapture.requests))
	}
	var localActivity f.Activity
	if json.Unmarshal(localCapture.requests[0], &localActivity) != nil || localActivity.Actor != actorURI || localActivity.Type != "Create" {
		t.Fatal("realm publisher did not emit a Group Create", string(localCapture.requests[0]))
	}
	var localNote f.Note
	if json.Unmarshal(localActivity.Object, &localNote) != nil || localNote.AttributedTo != actorURI {
		t.Fatal("realm post attribution is not the Group", string(localActivity.Object))
	}
	firstUpdate := now.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='first group update',updated_at=$2 WHERE id=$1`, postID, firstUpdate)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, postID, firstUpdate)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("first group update", n, reconcileErr)
	}
	secondUpdate := firstUpdate.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='second group update',updated_at=$2 WHERE id=$1`, postID, secondUpdate)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, postID, secondUpdate)
	parallel, cancelParallel := context.WithTimeout(x.ctx, 3*time.Second)
	defer cancelParallel()
	deliveryDone, reconcileDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, deliveryErr := x.r.ProcessRealmDeliveries(parallel, &federationCapture{}, 1)
		deliveryDone <- deliveryErr
	}()
	go func() {
		n, reconcileErr := x.r.ReconcilePosts(parallel, 10)
		if reconcileErr == nil && n != 1 {
			reconcileErr = errors.New("second group update not reconciled")
		}
		reconcileDone <- reconcileErr
	}()
	if deliveryErr, reconcileErr := <-deliveryDone, <-reconcileDone; deliveryErr != nil || reconcileErr != nil {
		t.Fatal("realm authority lock order", deliveryErr, reconcileErr)
	}
	if _, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, &federationCapture{}, 10); deliveryErr != nil {
		t.Fatal(deliveryErr)
	}
	deleted := secondUpdate.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET deleted_at=$2,updated_at=$2 WHERE id=$1`, postID, deleted)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'delete',$3)`, x.actor, postID, deleted)
	if n, err := x.r.ReconcilePosts(x.ctx, 10); err != nil || n != 1 {
		t.Fatal("realm withdrawal was not projected", n, err)
	}
	deleteCapture := &federationCapture{}
	if n, err := x.r.ProcessRealmDeliveries(x.ctx, deleteCapture, 10); err != nil || n != 1 || len(deleteCapture.requests) != 1 {
		t.Fatal("realm withdrawal was not delivered", n, err, len(deleteCapture.requests))
	}
	var deletion f.Activity
	if json.Unmarshal(deleteCapture.requests[0], &deletion) != nil || deletion.Type != "Delete" || deletion.Actor != actorURI {
		t.Fatal("realm withdrawal payload invalid", string(deleteCapture.requests[0]))
	}
	memberPost := authorityUUID(t)
	memberCreated := deleted.Add(time.Microsecond)
	query(t, x.db, `INSERT INTO owned_text_posts(id,publisher_id,realm_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,$3,'member secret after withdrawal',0,0,$4,$4,$4)`, memberPost, x.publisher, realmID, memberCreated)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'create',$3)`, x.actor, memberPost, memberCreated)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("member realm post projection", n, reconcileErr)
	}
	firstCapture := &federationCapture{}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, firstCapture, 10); deliveryErr != nil || n != 1 || len(firstCapture.requests) != 1 {
		t.Fatal("initial member Announce", n, deliveryErr)
	}
	var firstAnnounce f.Activity
	if json.Unmarshal(firstCapture.requests[0], &firstAnnounce) != nil || firstAnnounce.Type != "Announce" {
		t.Fatal("initial member activity", string(firstCapture.requests[0]))
	}
	memberUpdated := memberCreated.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='member secret after withdrawal updated',updated_at=$2 WHERE id=$1`, memberPost, memberUpdated)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, memberPost, memberUpdated)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("first pending member edit projection", n, reconcileErr)
	}
	memberUpdatedAgain := memberUpdated.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='member latest update',updated_at=$2 WHERE id=$1`, memberPost, memberUpdatedAgain)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, memberPost, memberUpdatedAgain)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("second pending member edit projection", n, reconcileErr)
	}
	memberCapture := &federationCapture{}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, memberCapture, 10); deliveryErr != nil || n != 2 || len(memberCapture.requests) != 2 {
		t.Fatal("coalesced member edit Undo/Announce delivery", n, deliveryErr, len(memberCapture.requests))
	}
	var editUndo f.Activity
	var editUndoneID string
	if json.Unmarshal(memberCapture.requests[0], &editUndo) != nil || editUndo.Type != "Undo" || json.Unmarshal(editUndo.Object, &editUndoneID) != nil || editUndoneID != firstAnnounce.ID {
		t.Fatal("edit did not first Undo exact prior Announce", string(memberCapture.requests[0]))
	}
	var announced f.Activity
	if json.Unmarshal(memberCapture.requests[1], &announced) != nil || announced.Type != "Announce" {
		t.Fatal("latest member edit was not re-announced", string(memberCapture.requests[1]))
	}
	var announcedNote f.Note
	if json.Unmarshal(announced.Object, &announcedNote) != nil || announcedNote.Content != "member latest update" {
		t.Fatal("intermediate pending Announce survived coalescing", string(announced.Object))
	}

	pendingDeletePost := authorityUUID(t)
	pendingDeleteCreated := memberUpdatedAgain.Add(time.Microsecond)
	query(t, x.db, `INSERT INTO owned_text_posts(id,publisher_id,realm_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,$3,'delete after pending edit',0,0,$4,$4,$4)`, pendingDeletePost, x.publisher, realmID, pendingDeleteCreated)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'create',$3)`, x.actor, pendingDeletePost, pendingDeleteCreated)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("pending-delete member create", n, reconcileErr)
	}
	pendingDeleteInitialCapture := &federationCapture{}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, pendingDeleteInitialCapture, 10); deliveryErr != nil || n != 1 || len(pendingDeleteInitialCapture.requests) != 1 {
		t.Fatal("pending-delete initial Announce", n, deliveryErr)
	}
	var pendingDeleteInitial f.Activity
	if json.Unmarshal(pendingDeleteInitialCapture.requests[0], &pendingDeleteInitial) != nil || pendingDeleteInitial.Type != "Announce" {
		t.Fatal("pending-delete initial activity", string(pendingDeleteInitialCapture.requests[0]))
	}
	pendingDeleteEdited := pendingDeleteCreated.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='pending edit must never become undo target',updated_at=$2 WHERE id=$1`, pendingDeletePost, pendingDeleteEdited)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, pendingDeletePost, pendingDeleteEdited)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("pending-delete edit projection", n, reconcileErr)
	}
	pendingDeleteRemoved := pendingDeleteEdited.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET deleted_at=$2,updated_at=$2 WHERE id=$1`, pendingDeletePost, pendingDeleteRemoved)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'delete',$3)`, x.actor, pendingDeletePost, pendingDeleteRemoved)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("delete superseding pending member edit", n, reconcileErr)
	}
	pendingDeleteCapture := &federationCapture{}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, pendingDeleteCapture, 10); deliveryErr != nil || n != 1 || len(pendingDeleteCapture.requests) != 1 {
		t.Fatal("pending-delete exact Undo delivery", n, deliveryErr, len(pendingDeleteCapture.requests))
	}
	var pendingDeleteUndo f.Activity
	var pendingDeleteTarget string
	if json.Unmarshal(pendingDeleteCapture.requests[0], &pendingDeleteUndo) != nil || pendingDeleteUndo.Type != "Undo" || json.Unmarshal(pendingDeleteUndo.Object, &pendingDeleteTarget) != nil || pendingDeleteTarget != pendingDeleteInitial.ID {
		t.Fatal("delete targeted an unseen pending Announce", string(pendingDeleteCapture.requests[0]), pendingDeleteInitial.ID)
	}

	uncertainEditPost := authorityUUID(t)
	uncertainEditCreated := pendingDeleteRemoved.Add(time.Microsecond)
	query(t, x.db, `INSERT INTO owned_text_posts(id,publisher_id,realm_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,$3,'uncertain edit first',0,0,$4,$4,$4)`, uncertainEditPost, x.publisher, realmID, uncertainEditCreated)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'create',$3)`, x.actor, uncertainEditPost, uncertainEditCreated)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("uncertain-edit create projection", n, reconcileErr)
	}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, &federationCapture{}, 10); deliveryErr != nil || n != 1 {
		t.Fatal("uncertain-edit initial Announce", n, deliveryErr)
	}
	uncertainEditSecond := uncertainEditCreated.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='uncertain edit second',updated_at=$2 WHERE id=$1`, uncertainEditPost, uncertainEditSecond)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, uncertainEditPost, uncertainEditSecond)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("uncertain-edit second projection", n, reconcileErr)
	}
	uncertainEditFailed := &federationCapture{codes: []int{202, 503}}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, uncertainEditFailed, 2); deliveryErr != nil || n != 2 || len(uncertainEditFailed.requests) != 2 {
		t.Fatal("uncertain-edit simulated remote acceptance", n, deliveryErr, len(uncertainEditFailed.requests))
	}
	var uncertainEditA2 f.Activity
	if json.Unmarshal(uncertainEditFailed.requests[1], &uncertainEditA2) != nil || uncertainEditA2.Type != "Announce" {
		t.Fatal("uncertain-edit second Announce", string(uncertainEditFailed.requests[1]))
	}
	uncertainEditThird := uncertainEditSecond.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='uncertain edit latest',updated_at=$2 WHERE id=$1`, uncertainEditPost, uncertainEditThird)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, uncertainEditPost, uncertainEditThird)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("uncertain-edit latest projection", n, reconcileErr)
	}
	uncertainEditCapture := &federationCapture{}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, uncertainEditCapture, 10); deliveryErr != nil || n != 2 || len(uncertainEditCapture.requests) != 2 {
		t.Fatal("uncertain-edit exact Undo/latest Announce", n, deliveryErr, len(uncertainEditCapture.requests))
	}
	var uncertainEditUndo, uncertainEditLatest f.Activity
	var uncertainEditTarget string
	if json.Unmarshal(uncertainEditCapture.requests[0], &uncertainEditUndo) != nil || uncertainEditUndo.Type != "Undo" || json.Unmarshal(uncertainEditUndo.Object, &uncertainEditTarget) != nil || uncertainEditTarget != uncertainEditA2.ID {
		t.Fatal("uncertain dispatched Announce was not withdrawn before replacement", string(uncertainEditCapture.requests[0]), uncertainEditA2.ID)
	}
	if json.Unmarshal(uncertainEditCapture.requests[1], &uncertainEditLatest) != nil || uncertainEditLatest.Type != "Announce" {
		t.Fatal("uncertain-edit latest Announce missing", string(uncertainEditCapture.requests[1]))
	}

	uncertainDeletePost := authorityUUID(t)
	uncertainDeleteCreated := uncertainEditThird.Add(time.Microsecond)
	query(t, x.db, `INSERT INTO owned_text_posts(id,publisher_id,realm_id,content,visibility,type,published_at,created_at,updated_at)VALUES($1,$2,$3,'uncertain delete first',0,0,$4,$4,$4)`, uncertainDeletePost, x.publisher, realmID, uncertainDeleteCreated)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'create',$3)`, x.actor, uncertainDeletePost, uncertainDeleteCreated)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("uncertain-delete create projection", n, reconcileErr)
	}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, &federationCapture{}, 10); deliveryErr != nil || n != 1 {
		t.Fatal("uncertain-delete initial Announce", n, deliveryErr)
	}
	uncertainDeleteSecond := uncertainDeleteCreated.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET content='uncertain delete second',updated_at=$2 WHERE id=$1`, uncertainDeletePost, uncertainDeleteSecond)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',$3)`, x.actor, uncertainDeletePost, uncertainDeleteSecond)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("uncertain-delete second projection", n, reconcileErr)
	}
	uncertainDeleteFailed := &federationCapture{codes: []int{202, 503}}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, uncertainDeleteFailed, 2); deliveryErr != nil || n != 2 || len(uncertainDeleteFailed.requests) != 2 {
		t.Fatal("uncertain-delete simulated remote acceptance", n, deliveryErr, len(uncertainDeleteFailed.requests))
	}
	var uncertainDeleteA2 f.Activity
	if json.Unmarshal(uncertainDeleteFailed.requests[1], &uncertainDeleteA2) != nil || uncertainDeleteA2.Type != "Announce" {
		t.Fatal("uncertain-delete second Announce", string(uncertainDeleteFailed.requests[1]))
	}
	uncertainDeleteRemoved := uncertainDeleteSecond.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET deleted_at=$2,updated_at=$2 WHERE id=$1`, uncertainDeletePost, uncertainDeleteRemoved)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'delete',$3)`, x.actor, uncertainDeletePost, uncertainDeleteRemoved)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("uncertain-delete withdrawal projection", n, reconcileErr)
	}
	uncertainDeleteCapture := &federationCapture{}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, uncertainDeleteCapture, 10); deliveryErr != nil || n != 1 || len(uncertainDeleteCapture.requests) != 1 {
		t.Fatal("uncertain-delete exact Undo", n, deliveryErr, len(uncertainDeleteCapture.requests))
	}
	var uncertainDeleteUndo f.Activity
	var uncertainDeleteTarget string
	if json.Unmarshal(uncertainDeleteCapture.requests[0], &uncertainDeleteUndo) != nil || uncertainDeleteUndo.Type != "Undo" || json.Unmarshal(uncertainDeleteUndo.Object, &uncertainDeleteTarget) != nil || uncertainDeleteTarget != uncertainDeleteA2.ID {
		t.Fatal("uncertain dispatched Announce survived delete", string(uncertainDeleteCapture.requests[0]), uncertainDeleteA2.ID)
	}

	query(t, x.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, x.actor)
	memberDeleted := uncertainDeleteRemoved.Add(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET deleted_at=$2,updated_at=$2 WHERE id=$1`, memberPost, memberDeleted)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'delete',$3)`, x.actor, memberPost, memberDeleted)
	if n, reconcileErr := x.r.ReconcilePosts(x.ctx, 10); reconcileErr != nil || n != 1 {
		t.Fatal("member withdrawal projection", n, reconcileErr)
	}
	undoCapture := &federationCapture{}
	if n, deliveryErr := x.r.ProcessRealmDeliveries(x.ctx, undoCapture, 10); deliveryErr != nil || n != 1 {
		t.Fatal("member Undo delivery", n, deliveryErr)
	}
	var undo f.Activity
	var undoneID string
	if json.Unmarshal(undoCapture.requests[0], &undo) != nil || undo.Type != "Undo" || json.Unmarshal(undo.Object, &undoneID) != nil || undoneID != announced.ID {
		t.Fatal("member withdrawal did not Undo exact Announce", string(undoCapture.requests[0]))
	}
	query(t, x.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, x.actor)
	receiveNote := func(note f.Note) error {
		create := x.activity(t, "Create", note)
		create.To = []string{f.Public}
		noteBody, _ := json.Marshal(create)
		noteRequest := httptest.NewRequest("POST", actorURI+"/inbox", bytes.NewReader(noteBody))
		if signErr := f.Sign(noteRequest, noteBody, x.keys.remote.KeyID, x.remoteKey, x.config.Clock()); signErr != nil {
			t.Fatal(signErr)
		}
		noteVerified, verifyErr := f.Verify(x.ctx, x.config, x.keys, noteRequest, noteBody)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		return x.r.ReceiveRealm(x.ctx, slug, noteVerified)
	}
	privateNote := f.Note{ID: x.keys.remote.Actor + "/notes/private-realm", Type: "Note", AttributedTo: x.keys.remote.Actor, Content: "must stay private", To: []string{actorURI}}
	if err = receiveNote(privateNote); !errors.Is(err, f.ErrForbidden) {
		t.Fatal("private Note was upgraded to public Announce", err)
	}
	crossOrigin := f.Note{ID: "https://other.example.test/notes/spoof", Type: "Note", AttributedTo: x.keys.remote.Actor, Content: "spoof", To: []string{f.Public}}
	if err = receiveNote(crossOrigin); !errors.Is(err, f.ErrForbidden) {
		t.Fatal("cross-origin Note ID admitted", err)
	}
	note := f.Note{ID: x.keys.remote.Actor + "/notes/realm", Type: "Note", AttributedTo: x.keys.remote.Actor, Content: "realm post", To: []string{f.Public}}
	if err = receiveNote(note); err != nil {
		t.Fatal(err)
	}
	outbox, err := x.r.RealmCollection(x.ctx, slug, "outbox", 0, 20)
	if err != nil || outbox.Total != 6 || len(outbox.Items) != 6 {
		t.Fatal(outbox, err)
	}
	outboxBody, _ := json.Marshal(outbox)
	if bytes.Contains(outboxBody, []byte("member secret after withdrawal")) {
		t.Fatal("withdrawn member content leaked from realm outbox", string(outboxBody))
	}
}
