// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"sync"
	"testing"
	"time"
)

func quoteRemoteNote(t *testing.T, x *federationFixture) f.Note {
	t.Helper()
	if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	var follow string
	if e := x.db.QueryRow(`SELECT follow_id FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, x.publisher, x.keys.remote.Actor).Scan(&follow); e != nil {
		t.Fatal(e)
	}
	if e := x.receive(t, x.activity(t, "Accept", follow)); e != nil {
		t.Fatal(e)
	}
	n := f.Note{ID: x.keys.remote.Actor + "/notes/" + authorityUUID(t), Type: "Note", AttributedTo: x.keys.remote.Actor, Content: "Synthetic remote public note", To: []string{f.Public}}
	// A public nested Note does not make its private envelope public.
	if e := x.receive(t, x.activity(t, "Create", n)); !errors.Is(e, f.ErrForbidden) {
		t.Fatal("private envelope with public Note must remain forbidden", e)
	}

	if e := x.receive(t, publicQuoteEnvelope(x.activity(t, "Create", n))); e != nil {
		t.Fatal(e)
	}
	return n
}
func quoteRequest(t *testing.T, x *federationFixture, n f.Note, target posts.Post) f.Activity {
	t.Helper()
	a := x.activity(t, "QuoteRequest", x.config.Origin+"/activitypub/objects/"+target.ID)
	a.Instrument, _ = json.Marshal(n.ID)
	return a
}
func quoteGrantID(t *testing.T, x *federationFixture, request f.Activity) string {
	t.Helper()
	var id string
	if e := x.db.QueryRow(`SELECT id::text FROM owned_federation_quote_authorizations WHERE publisher_id=$1 AND request_id=$2`, x.publisher, request.ID).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func TestPGFederationQuoteSignedLifecycleAndSingleObject(t *testing.T) {
	x := newFederationFixture(t, true)
	n := quoteRemoteNote(t, x)
	target := x.makePost(t, "Synthetic target", posts.Public, false)
	request := quoteRequest(t, x, n, target)
	if e := x.receive(t, request); e != nil {
		t.Fatal(e)
	}
	id := quoteGrantID(t, x, request)
	uri := x.config.Origin + "/quote-authorizations/" + id
	grant, e := x.r.QuoteAuthorization(x.ctx, id)
	if e != nil || grant.AttributedTo != x.actorURI || grant.InteractingObject != n.ID || grant.InteractionTarget != x.config.Origin+"/activitypub/objects/"+target.ID {
		t.Fatal(grant, e)
	}
	if e = x.receive(t, request); e != nil {
		t.Fatal("exact replay", e)
	}
	another := quoteRequest(t, x, n, target)
	if e = x.receive(t, another); !errors.Is(e, f.ErrReplay) {
		t.Fatal("second stamp for same object", e)
	}
	n.Quote, n.QuoteAuthorization = grant.InteractionTarget, uri
	if e = x.receive(t, publicQuoteEnvelope(x.activity(t, "Update", n))); e != nil {
		t.Fatal(e)
	}
	var consumed bool
	if e = x.db.QueryRow(`SELECT consumed_at IS NOT NULL FROM owned_federation_quote_authorizations WHERE id=$1`, id).Scan(&consumed); e != nil || !consumed {
		t.Fatal(consumed, e)
	}
	other := n
	other.ID = x.keys.remote.Actor + "/notes/" + authorityUUID(t)
	if e = x.receive(t, publicQuoteEnvelope(x.activity(t, "Create", other))); e == nil {
		t.Fatal("stamp reused for another object")
	}
	if w := x.call(t, "GET", "/quote-authorizations/"+id, "", false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	capture := &federationCapture{}
	if _, e = x.r.ProcessDeliveries(x.ctx, capture, 20); e != nil {
		t.Fatal(e)
	}
	if len(capture.requests) != 1 {
		t.Fatal("quote Accept was treated as Follow Accept", len(capture.requests))
	}
	var accept f.Activity
	if e = json.Unmarshal(capture.requests[0], &accept); e != nil || accept.Type != "Accept" || accept.Result != uri {
		t.Fatal(accept, e)
	}
	if w := x.call(t, "DELETE", "/quote-authorizations/"+id, "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := x.call(t, "POST", "/quote-authorizations", `{}`, true); w.Code != 422 {
		t.Fatal("unsigned grant path", w.Code)
	}
	if w := x.call(t, "DELETE", "/quote-authorizations/"+id, "", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if e = x.r.RevokeQuoteAuthorization(x.ctx, x.actor, id); e != nil {
		t.Fatal("idempotent revoke", e)
	}
	if _, e = x.r.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("revoked dereference", e)
	}
	if e = x.receive(t, publicQuoteEnvelope(x.activity(t, "Update", n))); e == nil {
		t.Fatal("revoked stamp accepted")
	}
	var raw []byte
	if e = x.db.QueryRow(`SELECT object FROM owned_federation_objects WHERE publisher_id=$1 AND object_id=$2`, x.publisher, n.ID).Scan(&raw); e != nil || strings.Contains(string(raw), "quoteAuthorization") {
		t.Fatal("cached approval retained", string(raw), e)
	}
	if _, e = x.r.ProcessDeliveries(x.ctx, capture, 20); e != nil {
		t.Fatal(e)
	}
	if len(capture.requests) != 2 {
		t.Fatal("missing one revocation notification", len(capture.requests))
	}
	var deletion f.Activity
	if e = json.Unmarshal(capture.requests[1], &deletion); e != nil || deletion.Type != "Delete" || strings.Contains(string(capture.requests[1]), "Synthetic target") {
		t.Fatal(deletion, e)
	}
	object, e := f.ObjectID(deletion.Object)
	if e != nil || object != uri {
		t.Fatal(object, e)
	}
}
func TestPGFederationQuoteCurrentPrivacyOriginAndOwnership(t *testing.T) {
	x := newFederationFixture(t, true)
	n := quoteRemoteNote(t, x)
	target := x.makePost(t, "Synthetic target", posts.Private, false)
	if e := x.receive(t, quoteRequest(t, x, n, target)); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("private quote grant", e)
	}
	target = x.makePost(t, "Public revision", posts.Public, false)
	bad := quoteRequest(t, x, n, target)
	bad.Instrument = json.RawMessage(`"https://other.example.test/notes/1"`)
	if e := x.receive(t, bad); e == nil {
		t.Fatal("untrusted origin")
	}
	unknown := n
	unknown.ID = x.keys.remote.Actor + "/notes/unknown"
	if e := x.receive(t, quoteRequest(t, x, unknown, target)); e == nil {
		t.Fatal("unknown object ownership")
	}
	request := quoteRequest(t, x, n, target)
	if e := x.receive(t, request); e != nil {
		t.Fatal(e)
	}
	id := quoteGrantID(t, x, request)
	visibility := posts.Private
	if _, e := x.post.Update(x.ctx, x.actor, target.ID, posts.Patch{Visibility: &visibility}); e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("private stamp exposed", e)
	}
	capture := &federationCapture{}
	if _, e := x.r.ProcessDeliveries(x.ctx, capture, 20); e != nil {
		t.Fatal(e)
	}
	if len(capture.requests) != 0 {
		t.Fatal("private target accepted remotely")
	}
	visibility = posts.Public
	if _, e := x.post.Update(x.ctx, x.actor, target.ID, posts.Patch{Visibility: &visibility}); e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("privacy ABA revived stamp", e)
	}
	punishedTarget := x.makePost(t, "Punished owner target", posts.Public, false)
	query(t, x.db, `INSERT INTO owned_authority_punishments(account_id,kind)VALUES($1,'disable_account')`, x.actor)
	if e := x.receive(t, quoteRequest(t, x, n, punishedTarget)); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("punished owner granted quote", e)
	}
	foreign := newFederationFixture(t, true).makePost(t, "Other publisher target", posts.Public, false)
	if e := x.receive(t, quoteRequest(t, x, n, foreign)); e == nil {
		t.Fatal("foreign target grant")
	}
}
func TestPGFederationQuoteFreshKeyAndBlockRevocation(t *testing.T) {
	x := newFederationFixture(t, true)
	n := quoteRemoteNote(t, x)
	target := x.makePost(t, "Synthetic key target", posts.Public, false)
	request := quoteRequest(t, x, n, target)
	proof := x.verified(t, request)
	now := x.clock.Load()
	x.clock.Store(now + 361)
	if e := x.r.Receive(x.ctx, x.name, proof); !errors.Is(e, f.ErrSignature) {
		t.Fatal("expired verified request", e)
	}
	x.clock.Store(now)
	keys := x.keys
	keys.remote.KeyID = x.keys.remote.Actor + "#rotated"
	changed, e := federationpg.New(x.db, x.config, keys, x.guard)
	if e != nil {
		t.Fatal(e)
	}
	if e = changed.Receive(x.ctx, x.name, proof); !errors.Is(e, f.ErrSignature) {
		t.Fatal("old key proof accepted after rotation", e)
	}
	if e = x.receive(t, request); e != nil {
		t.Fatal(e)
	}
	id := quoteGrantID(t, x, request)
	if _, e = changed.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("changed current key retained stamp", e)
	}
	if _, e = x.r.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("restoring the old key revived an observed invalid grant", e)
	}
	if e = x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "unfollow", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	newNote := quoteRemoteNote(t, x)
	if e = x.receive(t, publicQuoteEnvelope(x.activity(t, "Create", n))); e != nil {
		t.Fatal(e)
	}
	if _, e = x.r.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("unfollow and object reuse revived stamp", e)
	}
	request = quoteRequest(t, x, newNote, target)
	if e = x.receive(t, request); e != nil {
		t.Fatal(e)
	}
	id = quoteGrantID(t, x, request)
	if e = x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "block", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	if e = x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "unblock", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	var revoked bool
	if e = x.db.QueryRow(`SELECT revoked_at IS NOT NULL FROM owned_federation_quote_authorizations WHERE id=$1`, id).Scan(&revoked); e != nil || !revoked {
		t.Fatal("block did not permanently revoke", revoked, e)
	}
	if _, e = x.r.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("unblock revived stamp", e)
	}
}
func TestPGFederationQuoteConcurrentReplayAndRollback(t *testing.T) {
	x := newFederationFixture(t, true)
	n := quoteRemoteNote(t, x)
	target := x.makePost(t, "Synthetic rollback", posts.Public, false)
	request := quoteRequest(t, x, n, target)
	constraint := "quote_fail_" + strings.ReplaceAll(x.publisher, "-", "")
	query(t, x.db, `ALTER TABLE owned_federation_inbox ADD CONSTRAINT `+constraint+` CHECK(publisher_id<>'`+x.publisher+`'::uuid) NOT VALID`)
	if e := x.receive(t, request); e == nil {
		t.Fatal("fault accepted")
	}
	query(t, x.db, `ALTER TABLE owned_federation_inbox DROP CONSTRAINT `+constraint)
	if x.count(t, "owned_federation_quote_authorizations") != 0 {
		t.Fatal("grant survived inbox rollback")
	}
	proof := x.verified(t, request)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- x.r.Receive(context.Background(), x.name, proof) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if x.count(t, "owned_federation_quote_authorizations") != 1 {
		t.Fatal("concurrent duplicate grants")
	}
	id := quoteGrantID(t, x, request)
	query(t, x.db, `ALTER TABLE owned_federation_audit ADD CONSTRAINT `+constraint+` CHECK(publisher_id<>'`+x.publisher+`'::uuid) NOT VALID`)
	if e := x.r.RevokeQuoteAuthorization(x.ctx, x.actor, id); e == nil {
		t.Fatal("audit fault accepted")
	}
	query(t, x.db, `ALTER TABLE owned_federation_audit DROP CONSTRAINT `+constraint)
	if _, e := x.r.QuoteAuthorization(x.ctx, id); e != nil {
		t.Fatal("revocation partially committed", e)
	}
	var deletes int
	if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_deliveries WHERE quote_id=$1 AND activity_type='Delete'`, id).Scan(&deletes); e != nil || deletes != 0 {
		t.Fatal(deletes, e)
	}
	if e := x.issuer.Revoke(x.ctx, x.token, "current"); e != nil {
		t.Fatal(e)
	}
	if e := x.r.RevokeQuoteAuthorization(x.ctx, x.actor, id); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked local proof", e)
	}
}
func TestPGFederationQuoteRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	var saved struct {
		Publisher, Grant, Revoked string
		Rows, Deliveries          json.RawMessage
	}
	if phase == "before" {
		x := newFederationFixture(t, true)
		n := quoteRemoteNote(t, x)
		target := x.makePost(t, "Synthetic persisted quote", posts.Public, false)
		request := quoteRequest(t, x, n, target)
		if e := x.receive(t, request); e != nil {
			t.Fatal(e)
		}
		saved.Publisher, saved.Grant = x.publisher, quoteGrantID(t, x, request)
		other := n
		other.ID = x.keys.remote.Actor + "/notes/" + authorityUUID(t)
		if e := x.receive(t, publicQuoteEnvelope(x.activity(t, "Create", other))); e != nil {
			t.Fatal(e)
		}
		request = quoteRequest(t, x, other, target)
		if e := x.receive(t, request); e != nil {
			t.Fatal(e)
		}
		saved.Revoked = quoteGrantID(t, x, request)
		if e := x.r.RevokeQuoteAuthorization(x.ctx, x.actor, saved.Revoked); e != nil {
			t.Fatal(e)
		}
		// Isolate this fixture from unrelated global delivery scans. Preserve grant
		// and queued payload records exactly; after must not activate or reseed them.
		query(t, x.db, `UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1`, x.publisher)
		query(t, x.db, `UPDATE owned_federation_deliveries SET status='cancelled',lease_token=NULL,lease_until=NULL WHERE publisher_id=$1 AND status IN('pending','inflight')`, x.publisher)
		if e := x.db.QueryRow(`SELECT jsonb_agg(to_jsonb(g) ORDER BY id) FROM owned_federation_quote_authorizations g WHERE publisher_id=$1`, x.publisher).Scan(&saved.Rows); e != nil {
			t.Fatal(e)
		}
		if e := x.db.QueryRow(`SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM owned_federation_deliveries d WHERE publisher_id=$1 AND quote_id IS NOT NULL`, x.publisher).Scan(&saved.Deliveries); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, "federation-quote-restart.json", saved)
		return
	}
	db := database(t)
	loadRestartFixture(t, "federation-quote-restart.json", &saved)
	var rows, deliveries []byte
	if e := db.QueryRow(`SELECT jsonb_agg(to_jsonb(g) ORDER BY id) FROM owned_federation_quote_authorizations g WHERE publisher_id=$1`, saved.Publisher).Scan(&rows); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM owned_federation_deliveries d WHERE publisher_id=$1 AND quote_id IS NOT NULL`, saved.Publisher).Scan(&deliveries); e != nil {
		t.Fatal(e)
	}
	var same bool
	if e := db.QueryRow(`SELECT $1::jsonb=$2::jsonb AND $3::jsonb=$4::jsonb`, string(rows), string(saved.Rows), string(deliveries), string(saved.Deliveries)).Scan(&same); e != nil || !same {
		t.Fatal("durable quote state differs from saved prestate", same, e)
	}
	var active, revoked bool
	if e := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM owned_federation_quote_authorizations WHERE id=$1 AND revoked_at IS NULL),EXISTS(SELECT 1 FROM owned_federation_quote_authorizations WHERE id=$2 AND revoked_at IS NOT NULL)`, saved.Grant, saved.Revoked).Scan(&active, &revoked); e != nil || !active || !revoked {
		t.Fatal(active, revoked, e)
	}
}

// The gate is a local synthetic key lookup; it never performs remote discovery.
type quoteReviewGateKeys struct {
	f.KeyProvider
	entered, release chan struct{}
	once             sync.Once
}

func (k *quoteReviewGateKeys) RemoteActor(ctx context.Context, id string) (f.RemoteKey, error) {
	wait := false
	k.once.Do(func() { wait = true; close(k.entered) })
	if wait {
		select {
		case <-k.release:
		case <-ctx.Done():
			return f.RemoteKey{}, ctx.Err()
		}
	}
	return k.KeyProvider.RemoteActor(ctx, id)
}
func TestPGFederationQuoteReviewWaitsAndAcceptanceIsolation(t *testing.T) {
	for _, change := range []string{"privacy", "revocation"} {
		t.Run(change, func(t *testing.T) {
			x := newFederationFixture(t, true)
			n := quoteRemoteNote(t, x)
			target := x.makePost(t, "Synthetic current policy", posts.Public, false)
			request := quoteRequest(t, x, n, target)
			if e := x.receive(t, request); e != nil {
				t.Fatal(e)
			}
			id := quoteGrantID(t, x, request)
			gate := &quoteReviewGateKeys{KeyProvider: x.keys, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer release()
			r, e := federationpg.New(x.db, x.config, gate, x.guard)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := r.QuoteAuthorization(ctx, id); done <- e }()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("key lookup did not wait", ctx.Err())
			}
			if change == "privacy" {
				private := posts.Private
				if _, e = x.post.Update(ctx, x.actor, target.ID, posts.Patch{Visibility: &private}); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = x.r.RevokeQuoteAuthorization(ctx, x.actor, id); e != nil {
					t.Fatal(e)
				}
			}
			release()
			if e = <-done; !errors.Is(e, f.ErrNotFound) {
				t.Fatal("stale grant survived key lookup wait", change, e)
			}
		})
	}
	t.Run("follow-undo-does-not-cancel-quote", func(t *testing.T) {
		x := newFederationFixture(t, false)
		n := quoteRemoteNote(t, x)
		incoming := x.activity(t, "Follow", x.actorURI)
		if e := x.receive(t, incoming); e != nil {
			t.Fatal(e)
		}
		target := x.makePost(t, "Synthetic accepted quote", posts.Public, false)
		request := quoteRequest(t, x, n, target)
		if e := x.receive(t, request); e != nil {
			t.Fatal(e)
		}
		id := quoteGrantID(t, x, request)
		if e := x.receive(t, x.activity(t, "Undo", incoming)); e != nil {
			t.Fatal(e)
		}
		var pending int
		if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_deliveries WHERE quote_id=$1 AND activity_type='Accept' AND status='pending'`, id).Scan(&pending); e != nil || pending != 1 {
			t.Fatal("Follow Undo cancelled independent quote Accept", pending, e)
		}
		capture := &federationCapture{}
		if _, e := x.r.ProcessDeliveries(x.ctx, capture, 20); e != nil {
			t.Fatal(e)
		}
		if len(capture.requests) != 1 {
			t.Fatal("quote consent was not delivered", len(capture.requests))
		}
	})
	t.Run("revoke-after-egress-removal", func(t *testing.T) {
		x := newFederationFixture(t, true)
		n := quoteRemoteNote(t, x)
		target := x.makePost(t, "Synthetic removed peer", posts.Public, false)
		request := quoteRequest(t, x, n, target)
		if e := x.receive(t, request); e != nil {
			t.Fatal(e)
		}
		id := quoteGrantID(t, x, request)
		config := x.config
		config.AllowedOrigins = nil
		r, e := federationpg.New(x.db, config, x.keys, x.guard)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.RevokeQuoteAuthorization(x.ctx, x.actor, id); e != nil {
			t.Fatal("egress removal prevented local revocation", e)
		}
		if _, e = x.r.QuoteAuthorization(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
			t.Fatal("restored egress revived revoked grant", e)
		}
		var notifications int
		if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_deliveries WHERE quote_id=$1 AND activity_type='Delete'`, id).Scan(&notifications); e != nil || notifications != 0 {
			t.Fatal("notification queued for disallowed peer", notifications, e)
		}
	})
	for _, change := range []string{"audit", "expiry"} {
		t.Run("target-wait-"+change, func(t *testing.T) {
			x := newFederationFixture(t, true)
			n := quoteRemoteNote(t, x)
			target := x.makePost(t, "Synthetic waited target", posts.Public, false)
			request := quoteRequest(t, x, n, target)
			proof := x.verified(t, request)
			auditor := authorityUUID(t)
			if e := authoritypg.SeedSynthetic(x.ctx, x.db, auditor, "synthetic_quote_auditor_"+strings.ReplaceAll(auditor, "-", ""), time.Now()); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: x.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_text_posts", target.ID)
			done := make(chan error, 1)
			go func() { done <- x.r.Receive(ctx, x.name, proof) }()
			waitAtomicBlocked(t, locks, pid, "owned_text_posts")
			if change == "expiry" {
				x.clock.Add(361)
			} else {
				if _, e := blocker.ExecContext(ctx, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at)VALUES($1,$2,'update',clock_timestamp())`, auditor, target.ID); e != nil {
					t.Fatal(e)
				}
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			e := <-done
			if change == "expiry" {
				if !errors.Is(e, f.ErrSignature) {
					t.Fatal("expired signed proof admitted after target wait", e)
				}
				if x.count(t, "owned_federation_quote_authorizations") != 0 {
					t.Fatal("expired request left a grant")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			id := quoteGrantID(t, x, request)
			var current bool
			if e = x.db.QueryRow(`SELECT target_audit_id=(SELECT max(a.id) FROM owned_text_post_audit a WHERE a.post_id=g.target_post_id) FROM owned_federation_quote_authorizations g WHERE id=$1`, id).Scan(&current); e != nil || !current {
				t.Fatal("waited target audit snapshot was stale", current, e)
			}
			if _, e = x.r.QuoteAuthorization(x.ctx, id); e != nil {
				t.Fatal("new grant was immediately stale", e)
			}
		})
	}
}

func publicQuoteEnvelope(a f.Activity) f.Activity {
	a.To = []string{f.Public}
	return a
}
func TestFederationQuoteFixturePublicEnvelope(t *testing.T) {
	note := f.Note{ID: "https://remote.example.test/notes/synthetic", Type: "Note", AttributedTo: "https://remote.example.test/actors/synthetic", To: []string{f.Public}}
	raw, e := json.Marshal(note)
	if e != nil {
		t.Fatal(e)
	}
	private := f.Activity{Type: "Create", Actor: note.AttributedTo, Object: raw, To: []string{"https://local.example.test/activitypub/actors/synthetic"}}
	if f.IsPublic(private) {
		t.Fatal("nested public Note widened private envelope")
	}
	wire, e := json.Marshal(publicQuoteEnvelope(private))
	if e != nil {
		t.Fatal(e)
	}
	var decoded f.Activity
	if e = json.Unmarshal(wire, &decoded); e != nil {
		t.Fatal(e)
	}
	if !f.IsPublic(decoded) {
		t.Fatal("public fixture envelope lost public audience")
	}
	var nested f.Note
	if e = json.Unmarshal(decoded.Object, &nested); e != nil || !f.Has(nested.To, f.Public) {
		t.Fatal("nested public audience lost", e)
	}
}
