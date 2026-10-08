// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationhttp"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/postpg"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	federation "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This double has no private key and cannot produce a valid RSA signature. It
// tests identifier/routing continuity through an in-memory transport only.
type renamePublicOnlySigner struct{ public *rsa.PublicKey }

func (s renamePublicOnlySigner) Public() crypto.PublicKey { return s.public }
func (renamePublicOnlySigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return []byte("synthetic identifier-routing signature"), nil
}

type renameKeys struct {
	publisher string
	signer    renamePublicOnlySigner
	remote    federation.RemoteKey
	reads     atomic.Int64
}

func (k *renameKeys) LocalKey(_ context.Context, id string) (crypto.Signer, error) {
	k.reads.Add(1)
	if id != k.publisher {
		return nil, federation.ErrUnavailable
	}
	return k.signer, nil
}
func (k *renameKeys) RemoteActor(_ context.Context, id string) (federation.RemoteKey, error) {
	if id != k.remote.Actor {
		return federation.RemoteKey{}, federation.ErrNotFound
	}
	return k.remote, nil
}
func (k *renameKeys) RemoteKey(_ context.Context, id string) (federation.RemoteKey, error) {
	if id != k.remote.KeyID {
		return federation.RemoteKey{}, federation.ErrNotFound
	}
	return k.remote, nil
}

type renameFixture struct {
	nameCardFixture
	federation           *federationpg.Repository
	keys                 *renameKeys
	policy               federation.Config
	http                 http.Handler
	oldName, stableActor string
	preserve             *bool
}

func newRenameFixture(t *testing.T, enabled bool) renameFixture {
	t.Helper()
	f := renameFixture{nameCardFixture: newNameCardFixture(t)}
	keep := false
	f.preserve = &keep
	query(t, f.db, federationpg.Schema)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	if e := f.db.QueryRow(`SELECT name FROM owned_publishers WHERE id=$1::uuid`, f.publisher).Scan(&f.oldName); e != nil {
		t.Fatal(e)
	}
	f = configureRename(t, f)
	if enabled {
		if e := f.federation.Settings(f.ctx, f.actor, f.publisher, federation.Settings{Enabled: true, Manual: true}); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() {
		if *f.preserve {
			return
		}
		_, _ = f.db.Exec(`UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1::uuid`, f.publisher)
		_, _ = f.db.Exec(`UPDATE owned_federation_deliveries SET status='cancelled',lease_token=NULL,lease_until=NULL WHERE publisher_id=$1::uuid AND status IN('pending','inflight')`, f.publisher)
	})
	return f
}
func configureRename(t *testing.T, f renameFixture) renameFixture {
	t.Helper()
	n := new(big.Int).Lsh(big.NewInt(1), 2047)
	n.Add(n, big.NewInt(267))
	public := &rsa.PublicKey{N: n, E: 65537}
	f.keys = &renameKeys{publisher: f.publisher, signer: renamePublicOnlySigner{public}, remote: federation.RemoteKey{Actor: "https://remote.example.test/actors/" + f.publisher, Inbox: "https://remote.example.test/inbox", KeyID: "https://remote.example.test/actors/" + f.publisher + "#main-key", PublicKey: public}}
	f.policy = federation.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, Now: f.config.Now, MaxAttempts: 3, DeliveryTimeout: time.Second}
	var e error
	f.federation, e = federationpg.New(f.db, f.policy, f.keys, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.federation.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.http = federationhttp.Handler{Repository: f.federation, Config: f.policy, Keys: f.keys, Authority: f.verifier}
	f.stableActor = f.policy.Origin + "/activitypub/actors/" + f.oldName
	return f
}
func renameCall(t *testing.T, f renameFixture, path string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := fixtureNoBodyRequest("GET", f.policy.Origin+path, "")
	w := httptest.NewRecorder()
	f.http.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cache unsafe alias")
	}
	return w
}
func renamedCard(t *testing.T, f renameFixture, name, key string) namecards.Purchase {
	t.Helper()
	v, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: name}, key)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGFederatedRenameStableCachedURLsAndCardReplay(t *testing.T) {
	f := newRenameFixture(t, true)
	before, e := f.federation.Actor(f.ctx, f.oldName)
	if e != nil {
		t.Fatal(e)
	}
	postRepo := postpg.NewWithAuthority(f.db, f.guard)
	fixture := &federationFixture{db: f.db, post: postRepo, actor: f.actor, publisher: f.publisher, ctx: f.ctx, config: f.policy}
	post := fixture.makePost(t, "Synthetic cached publisher URL", posts.Public, false)
	slug := "cached-slug"
	query(t, f.db, `UPDATE owned_text_posts SET slug=$2::text WHERE id=$1::uuid`, post.ID, slug)
	paidNameCard(t, f.nameCardFixture, "alias-order")
	name := "renamed_" + f.publisher
	used := renamedCard(t, f, name, "alias-use")
	if replay, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: name}, "alias-use"); e != nil || replay.ID != used.ID {
		t.Fatal("card replay", replay, e)
	}
	for _, handle := range []string{f.oldName, name, strings.ToUpper(f.oldName)} {
		a := backendDecode[federation.Actor](t, renameCall(t, f, "/activitypub/actors/"+handle, 200))
		if a.ID != before.ID || a.PublicKey != before.PublicKey || a.Username != name || a.Inbox != before.Inbox {
			t.Fatal("actor or key identity moved", a)
		}
		renameCall(t, f, "/activitypub/actors/"+handle+"/outbox", 200)
		w := renameCall(t, f, "/.well-known/webfinger?resource="+url.QueryEscape("acct:"+handle+"@local.example.test"), 200)
		if !strings.Contains(w.Body.String(), before.ID) || !strings.Contains(w.Body.String(), "acct:"+name+"@local.example.test") {
			t.Fatal(w.Body.String())
		}
	}
	if n, e := f.federation.Object(f.ctx, post.ID); e != nil || n.AttributedTo != before.ID {
		t.Fatal("object attribution changed", n, e)
	}
	if p, e := postRepo.GetSlug(f.ctx, "", f.oldName, slug); e != nil || p.ID != post.ID {
		t.Fatal("cached post slug URL lost", p, e)
	}
	a := accountpg.NewWithAuthority(f.db, f.guard)
	nick := "Synthetic new display name"
	if _, e = a.UpdatePublisher(f.ctx, f.actor, f.oldName, accounts.PublisherPatch{Nick: &nick}); e != nil {
		t.Fatal(e)
	}
	current, e := f.federation.Actor(f.ctx, name)
	if e != nil || current.Name != nick || current.ID != before.ID || current.PublicKey != before.PublicKey {
		t.Fatal("display-name edit moved identity", current, e)
	}
	if count := commerceCount(t, f.db, `SELECT count(*) FROM owned_publisher_name_reservations WHERE publisher_id=$1::uuid`, f.publisher); count != 2 {
		t.Fatal("display edit added alias", count)
	}
	private := posts.Private
	if _, e = postRepo.Update(f.ctx, f.actor, post.ID, posts.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	renameCall(t, f, "/activitypub/objects/"+post.ID, 404)
	if _, e = postRepo.GetSlug(f.ctx, "", f.oldName, slug); e == nil {
		t.Fatal("old slug bypassed private post")
	}
}
func TestPGFederatedRenameExplicitOriginInitialization(t *testing.T) {
	f := newRenameFixture(t, false)
	query(t, f.db, `INSERT INTO owned_federation_publishers(publisher_id,enabled,manual,updated_at)VALUES($1::uuid,false,true,now())`, f.publisher)
	paidNameCard(t, f.nameCardFixture, "legacy-order")
	if _, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: "legacy_" + f.publisher}, "legacy-use"); !errors.Is(e, namecards.ErrActorMigration) {
		t.Fatal("uninitialized identity renamed", e)
	}
	reads := f.keys.reads.Load()
	if e := f.federation.InitializeIdentity(f.ctx, f.publisher); e != nil {
		t.Fatal(e)
	}
	if f.keys.reads.Load() != reads {
		t.Fatal("initializer loaded key material")
	}
	if e := f.federation.InitializeIdentity(f.ctx, f.publisher); e != nil {
		t.Fatal("initialization replay", e)
	}
	otherPolicy := f.policy
	otherPolicy.Origin = "https://other.example.test"
	other, e := federationpg.New(f.db, otherPolicy, nil, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	if e = other.InitializeIdentity(f.ctx, f.publisher); !errors.Is(e, federation.ErrInvalid) {
		t.Fatal("origin rewritten", e)
	}
	if _, e = other.InitializeIdentities(f.ctx, 10); !errors.Is(e, federation.ErrInvalid) {
		t.Fatal("batch origin reinitialization accepted", e)
	}
	if _, e = f.db.Exec(`UPDATE owned_federation_publishers SET actor_uri='https://other.example.test/actor',key_id='https://other.example.test/actor#main-key' WHERE publisher_id=$1::uuid`, f.publisher); e == nil {
		t.Fatal("identity anchor mutable")
	}
	rename := renamedCard(t, f, "legacy_"+f.publisher, "legacy-use")
	if rename.ConsumedAt == nil {
		t.Fatal(rename)
	}
}
func TestPGFederatedRenameClaimsCapAndRetainedOwnership(t *testing.T) {
	f := newRenameFixture(t, true)
	g := newRenameFixture(t, true)
	paidNameCard(t, f.nameCardFixture, "claim-f")
	paidNameCard(t, g.nameCardFixture, "claim-g")
	target := "shared_" + authorityUUID(t)
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, v := range []renameFixture{f, g} {
		go func(v renameFixture) {
			<-start
			_, e := v.cards.Use(v.ctx, v.actor, namecards.UseRequest{Target: "publisher", TargetID: &v.publisher, NewName: target}, "claim-use")
			done <- e
		}(v)
	}
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		e := <-done
		if e == nil {
			wins++
		} else if !errors.Is(e, namecards.ErrUse) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("concurrent alias claims", wins)
	}
	var winner string
	if e := f.db.QueryRow(`SELECT publisher_id::text FROM owned_publisher_name_reservations WHERE folded_name=lower($1::text)`, target).Scan(&winner); e != nil {
		t.Fatal(e)
	}
	var winnerFixture, loser renameFixture
	if winner == f.publisher {
		winnerFixture, loser = f, g
	} else {
		winnerFixture, loser = g, f
	}
	old := winnerFixture.oldName
	otherName := "Another synthetic publisher"
	if _, e := accountpg.NewWithAuthority(loser.db, loser.guard).CreatePublisher(loser.ctx, loser.actor, accounts.PublisherPatch{Name: &old, Nick: &otherName}); !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("retired alias reassigned", e)
	}
	if e := accountpg.NewWithAuthority(winnerFixture.db, winnerFixture.guard).DeletePublisher(winnerFixture.ctx, winnerFixture.actor, winner); e != nil {
		t.Fatal(e)
	}
	renameCall(t, winnerFixture, "/activitypub/actors/"+old, 404)
	if _, e := accountpg.NewWithAuthority(loser.db, loser.guard).CreatePublisher(loser.ctx, loser.actor, accounts.PublisherPatch{Name: &old, Nick: &otherName}); !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("deleted alias reassigned", e)
	}
	cap := newRenameFixture(t, true)
	paidNameCard(t, cap.nameCardFixture, "cap-order")
	query(t, cap.db, `INSERT INTO owned_publisher_name_reservations(folded_name,name,publisher_id,created_at)SELECT 'cap_'||i::text||'_'||$1::text,'cap_'||i::text||'_'||$1::text,$1::uuid,now()FROM generate_series(1,255)i`, cap.publisher)
	if _, e := cap.cards.Use(cap.ctx, cap.actor, namecards.UseRequest{Target: "publisher", TargetID: &cap.publisher, NewName: "over_cap_" + cap.publisher}, "cap-fail"); !errors.Is(e, namecards.ErrUse) {
		t.Fatal("alias cap ignored", e)
	}
	if count := commerceCount(t, cap.db, `SELECT count(*) FROM owned_name_card_purchases WHERE account_id=$1::uuid AND consumed_at IS NULL`, cap.actor); count != 1 {
		t.Fatal("cap failure spent card")
	}
	reused := "cap_1_" + cap.publisher
	renamedCard(t, cap, reused, "cap-reuse")
	if count := commerceCount(t, cap.db, `SELECT count(*) FROM owned_publisher_name_reservations WHERE publisher_id=$1::uuid`, cap.publisher); count != 256 {
		t.Fatal(count)
	}
}

type renameCapture struct{ actors, keys []string }

func (s *renameCapture) Send(_ context.Context, r *http.Request) (int, error) {
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return 0, e
	}
	var a federation.Activity
	if e = json.Unmarshal(b, &a); e != nil {
		return 0, e
	}
	s.actors = append(s.actors, a.Actor)
	s.keys = append(s.keys, r.Header.Get("Signature"))
	return 202, nil
}
func TestPGFederatedRenameQueuedDeliveryAndCurrentPrivacy(t *testing.T) {
	f := newRenameFixture(t, true)
	if e := f.federation.Change(f.ctx, f.actor, f.publisher, federation.Change{Action: "follow", RemoteActor: f.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	paidNameCard(t, f.nameCardFixture, "queued-order")
	name := "queued_" + f.publisher
	renamedCard(t, f, name, "queued-use")
	sender := &renameCapture{}
	if _, e := f.federation.ProcessDeliveries(f.ctx, sender, 100); e != nil {
		t.Fatal(e)
	}
	if len(sender.actors) != 1 || sender.actors[0] != f.stableActor || !strings.Contains(sender.keys[0], `keyId="`+f.stableActor+`#main-key"`) {
		t.Fatal("queued identity/key changed", sender)
	}
	if _, e := f.federation.ProcessDeliveries(f.ctx, sender, 100); e != nil || len(sender.actors) != 1 {
		t.Fatal("sent activity replay", e)
	}
	for _, state := range []string{"disabled", "shadowbanned", "deleted"} {
		switch state {
		case "disabled":
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
		case "shadowbanned":
			query(t, f.db, `UPDATE owned_publishers SET shadowbanned_at=now(),shadowban_reason='spam' WHERE id=$1::uuid`, f.publisher)
		case "deleted":
			query(t, f.db, `UPDATE owned_publishers SET deleted_at=now() WHERE id=$1::uuid`, f.publisher)
		}
		for _, handle := range []string{f.oldName, name} {
			renameCall(t, f, "/activitypub/actors/"+handle, 404)
			renameCall(t, f, "/.well-known/webfinger?resource="+url.QueryEscape("acct:"+handle+"@local.example.test"), 404)
		}
		switch state {
		case "disabled":
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, f.actor)
		case "shadowbanned":
			query(t, f.db, `UPDATE owned_publishers SET shadowbanned_at=NULL,shadowban_reason=NULL WHERE id=$1::uuid`, f.publisher)
		}
	}
}
func TestPGFederatedRenameRollbackAndPrivilegedSharedPolicy(t *testing.T) {
	f := newRenameFixture(t, true)
	card := paidNameCard(t, f.nameCardFixture, "rollback-order")
	query(t, f.db, `CREATE FUNCTION synthetic_alias_card_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.account_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic rename rollback';END IF;RETURN NEW;END$$`)
	query(t, f.db, `CREATE TRIGGER synthetic_alias_card_fail BEFORE INSERT ON owned_name_card_audit FOR EACH ROW EXECUTE FUNCTION synthetic_alias_card_fail('`+f.actor+`')`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_alias_card_fail ON owned_name_card_audit;DROP FUNCTION IF EXISTS synthetic_alias_card_fail()`)
	})
	name := "rollback_" + f.publisher
	if _, e := f.cards.Use(f.ctx, f.actor, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: name}, "rollback-use"); e == nil {
		t.Fatal("card audit failure hidden")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_publisher_name_reservations WHERE folded_name=lower($1::text)`, name); n != 0 {
		t.Fatal("alias partially reserved")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_name_card_purchases WHERE id=$1::uuid AND consumed_at IS NULL`, card.PurchaseID); n != 1 {
		t.Fatal("failed rename spent card")
	}
	a, e := f.federation.Actor(f.ctx, f.oldName)
	if e != nil || a.Username != f.oldName || a.ID != f.stableActor {
		t.Fatal(a, e)
	}
	query(t, f.db, `DROP TRIGGER synthetic_alias_card_fail ON owned_name_card_audit`)
	query(t, f.db, moderationpg.Schema)
	moderator, e := moderationpg.New(f.db, f.guard, moderationpg.Config{Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	request := admin.Command{Action: "update", Publisher: &admin.PublisherPatch{Name: &name}}
	if _, e = moderator.Mutate(f.ctx, f.actor, admin.PublisherKind, f.publisher, request); e == nil {
		t.Fatal("ordinary role gained admin rename")
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.actor)
	if _, e = moderator.Mutate(f.ctx, f.actor, admin.PublisherKind, f.publisher, request); e != nil {
		t.Fatal(e)
	}
	a, e = f.federation.Actor(f.ctx, f.oldName)
	if e != nil || a.Username != name || a.ID != f.stableActor {
		t.Fatal("admin bypassed identity policy", a, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_name_card_purchases WHERE id=$1::uuid AND consumed_at IS NULL`, card.PurchaseID); n != 1 {
		t.Fatal("privileged rename consumed user card")
	}
}
func TestPGFederatedRenameRestartPersistence(t *testing.T) {
	_ = database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "federation-rename-record.json")
	type saved struct{ Actor, Publisher, OldName, NewName, ActorURI, KeyID string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newRenameFixture(t, true)
		paidNameCard(t, f.nameCardFixture, "restart-order")
		name := "restart_alias_" + f.publisher
		renamedCard(t, f, name, "restart-use")
		a, e := f.federation.Actor(f.ctx, f.oldName)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(saved{f.actor, f.publisher, f.oldName, name, a.ID, a.PublicKey.ID})
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		*f.preserve = true
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var data saved
	if e = json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	f := newRenameFixture(t, false)
	f.actor = data.Actor
	f.publisher = data.Publisher
	f.oldName = data.OldName
	f.token = pgIssue(t, f.issuer, f.actor).Token
	p, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = f.guard.Bind(f.ctx, p)
	f = configureRename(t, f)
	for _, handle := range []string{data.OldName, data.NewName} {
		a, e := f.federation.Actor(f.ctx, handle)
		if e != nil || a.ID != data.ActorURI || a.PublicKey.ID != data.KeyID || a.Username != data.NewName {
			t.Fatal("restart identity drift", a, e)
		}
	}
}
