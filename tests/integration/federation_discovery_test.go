// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/federationhttp"
	"solsynth.local/go-migration/internal/federationpg"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/passport/accounts"
)

type discoveryFetchFunc func(context.Context, string, string) (f.FetchResponse, error)

func (fn discoveryFetchFunc) Fetch(c context.Context, u, a string) (f.FetchResponse, error) {
	return fn(c, u, a)
}
func discoveryFixture(t *testing.T) (*federationFixture, f.DiscoveryTarget) {
	t.Helper()
	x := newFederationFixture(t, true)
	query(t, x.db, federationpg.DiscoverySchema)
	target := f.DiscoveryTarget{Resource: "acct:" + x.name + "@remote.example.test", Actor: x.keys.remote.Actor}
	r, e := federationpg.NewWithDiscovery(x.db, x.config, x.keys, x.guard, f.DiscoveryFeatures{Targets: []f.DiscoveryTarget{target}})
	if e != nil {
		t.Fatal(e)
	}
	x.r = r
	x.handler = federationhttp.Handler{Repository: r, Config: x.config, Keys: x.keys, Authority: x.verifier}
	t.Cleanup(func() {
		query(t, x.db, `UPDATE owned_federation_discoveries SET status='cancelled',lease_token=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE publisher_id=$1 AND status IN('pending','inflight')`, x.publisher)
	})
	return x, target
}
func discoveryResponse(t *testing.T, x *federationFixture, target f.DiscoveryTarget) discoveryFetchFunc {
	t.Helper()
	public, e := f.PEM(x.remoteKey)
	if e != nil {
		t.Fatal(e)
	}
	return func(ctx context.Context, uri, accept string) (f.FetchResponse, error) {
		if _, ok := ctx.Deadline(); !ok {
			return f.FetchResponse{}, errors.New("missing deadline")
		}
		var value any
		switch uri {
		case target.WebFingerURL():
			value = map[string]any{"subject": target.Resource, "links": []any{map[string]string{"rel": "self", "type": f.MediaType, "href": target.Actor}}}
		case target.Actor:
			value = map[string]any{"id": target.Actor, "type": "Person", "preferredUsername": x.name, "name": "Synthetic remote", "summary": "<script>untrusted</script>", "inbox": x.keys.remote.Inbox, "publicKey": f.PublicKey{ID: x.keys.remote.KeyID, Owner: target.Actor, PEM: public}}
		default:
			return f.FetchResponse{}, f.ErrForbidden
		}
		body, _ := json.Marshal(value)
		return f.FetchResponse{Status: 200, ContentType: accept, Body: body}, nil
	}
}
func discoveryRequest(t *testing.T, x *federationFixture, target f.DiscoveryTarget) f.DiscoveryJob {
	t.Helper()
	job, e := x.r.RequestDiscovery(x.ctx, x.actor, x.publisher, target.Resource)
	if e != nil {
		t.Fatal(e)
	}
	return job
}
func discoveryState(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if e := db.QueryRow(`SELECT status FROM owned_federation_discoveries WHERE id=$1`, id).Scan(&s); e != nil {
		t.Fatal(e)
	}
	return s
}
func staticDiscoveryServer(t *testing.T) f.ServerConfig {
	t.Helper()
	// Deliberately non-secret, fixed synthetic public modulus; no signer or
	// deployment credential can be constructed from this public-only fixture.
	public := &rsa.PublicKey{N: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 2047), big.NewInt(31337)), E: 65537}
	der, e := x509.MarshalPKIXPublicKey(public)
	if e != nil {
		t.Fatal(e)
	}
	return f.ServerConfig{Username: "synthetic-server-discovery-permanent", Name: "Synthetic server", Summary: "<b>server</b>", PublicKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
}
func discoveryServerRepo(t *testing.T, x *federationFixture, server f.ServerConfig) *federationpg.Repository {
	t.Helper()
	r, e := federationpg.NewWithDiscovery(x.db, x.config, x.keys, x.guard, f.DiscoveryFeatures{Server: &server})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestPGFederationServerDiscoveryAndReservedIdentity(t *testing.T) {
	x, _ := discoveryFixture(t)
	server := staticDiscoveryServer(t)
	r := discoveryServerRepo(t, x, server)
	if e := r.InitializeServerIdentity(x.ctx); e != nil {
		t.Fatal(e)
	}
	if e := r.InitializeServerIdentity(x.ctx); e != nil {
		t.Fatal("idempotent initialize", e)
	}
	h := federationhttp.Handler{Repository: r, Config: x.config, Keys: x.keys, Authority: x.verifier}
	for _, path := range []string{"/activitypub/actor", "/activitypub/actor/main-key", "/activitypub/actor/outbox?page=true", "/activitypub/actor/followers", "/activitypub/actor/following", "/.well-known/host-meta", "/.well-known/webfinger?resource=acct:" + strings.ToUpper(server.Username) + "@local.example.test", "/activitypub/actors/" + x.name + "/main-key", "/activitypub/actors/" + x.name + "/publickey"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", x.config.Origin+path, nil))
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	actor, e := r.ServerActor(x.ctx)
	if e != nil || actor.PublicKey.Owner != actor.ID || actor.Type != "Application" {
		t.Fatal(actor, e)
	}
	changed := server
	changed.Username = "synthetic-server-changed"
	if e = discoveryServerRepo(t, x, changed).InitializeServerIdentity(x.ctx); !errors.Is(e, f.ErrForbidden) {
		t.Fatal("server handle reanchored", e)
	}
	changed = server
	changed.PublicKeyPEM, _ = f.PEM(x.remoteKey)
	if _, e = discoveryServerRepo(t, x, changed).ServerActor(x.ctx); !errors.Is(e, f.ErrUnavailable) {
		t.Fatal("changed key served", e)
	}
	conflict := server
	conflict.Username = x.name
	if e = discoveryServerRepo(t, x, conflict).InitializeServerIdentity(x.ctx); e == nil {
		t.Fatal("publisher name captured")
	}
	name := server.Username
	if _, e = accountpg.NewForCompatibilityTests(x.db).CreatePublisher(x.ctx, x.actor, accounts.PublisherPatch{Name: &name, Nick: &name}); e == nil {
		t.Fatal("publisher captured server name")
	}
	if _, e = x.db.Exec(`UPDATE owned_federation_server_identity SET username='synthetic-replacement' WHERE singleton`); e == nil {
		t.Fatal("server anchor mutated")
	}
	if _, e = x.db.Exec(`DELETE FROM owned_federation_server_identity WHERE singleton`); e == nil {
		t.Fatal("server anchor deleted")
	}
	// Publisher key aliases retain the preexisting actor/key identity after rename.
	original, e := x.r.Actor(x.ctx, x.name)
	if e != nil {
		t.Fatal(e)
	}
	renamed := x.name + "_renamed"
	query(t, x.db, `UPDATE owned_publishers SET name=$2,updated_at=clock_timestamp() WHERE id=$1`, x.publisher, renamed)
	for _, handle := range []string{x.name, renamed} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", x.config.Origin+"/activitypub/actors/"+handle+"/main-key", nil))
		var key f.KeyDocument
		if e = json.Unmarshal(w.Body.Bytes(), &key); e != nil || w.Code != 200 || key.ID != original.PublicKey.ID || key.Owner != original.ID {
			t.Fatal(handle, w.Code, key, e)
		}
	}
}
func TestPGFederationDiscoveryLifecycleAndIsolation(t *testing.T) {
	x, target := discoveryFixture(t)
	first := discoveryRequest(t, x, target)
	replay := discoveryRequest(t, x, target)
	if first.ID != replay.ID || replay.State != "pending" {
		t.Fatal(first, replay)
	}
	var count int
	if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_audit WHERE publisher_id=$1 AND action='discovery.request'`, x.publisher).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate audit", count, e)
	}
	if _, e := x.r.RequestDiscovery(context.Background(), x.actor, x.publisher, target.Resource); e == nil {
		t.Fatal("unbound proof accepted")
	}
	if _, e := x.r.RequestDiscovery(x.ctx, x.actor, x.publisher, "acct:arbitrary@remote.example.test"); !errors.Is(e, f.ErrForbidden) {
		t.Fatal("unconfigured lookup", e)
	}
	if _, e := x.r.ProcessDiscoveries(x.ctx, nil, 1); !errors.Is(e, f.ErrUnavailable) {
		t.Fatal(e)
	}
	var calls int
	response := discoveryResponse(t, x, target)
	fetch := discoveryFetchFunc(func(ctx context.Context, u, a string) (f.FetchResponse, error) { calls++; return response(ctx, u, a) })
	if n, e := x.r.ProcessDiscoveries(x.ctx, fetch, 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	result, e := x.r.Discovery(x.ctx, x.actor, x.publisher, target.Resource)
	if e != nil || result.State != "succeeded" || result.Attempts != 1 || result.Result == nil || result.Result.ID != target.Actor || result.Result.Summary != "&lt;script&gt;untrusted&lt;/script&gt;" || calls != 2 {
		t.Fatal(result, calls, e)
	}
	if _, e = x.r.ProcessDiscoveries(x.ctx, fetch, 1); e != nil || calls != 2 {
		t.Fatal("completed job fetched again", e, calls)
	}
	for _, table := range []string{"owned_federation_peers", "owned_federation_relationships", "owned_federation_objects", "owned_federation_inbox", "owned_federation_deliveries"} {
		var n int
		if e = x.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE actor_uri=$1`, target.Actor).Scan(&n); e != nil || n != 0 {
			t.Fatal("discovery promoted observation", table, n, e)
		}
	}
	endpoint := "/api/activitypub/publishers/" + x.publisher + "/discovery?resource=" + url.QueryEscape(target.Resource)
	if w := x.call(t, "GET", endpoint, "", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := x.call(t, "GET", endpoint, "", false); w.Code != 401 {
		t.Fatal("private lookup", w.Code)
	}
	if _, e = x.db.Exec(`UPDATE owned_federation_discoveries SET actor_uri='https://remote.example.test/other' WHERE id=$1`, first.ID); e == nil {
		t.Fatal("immutable request rewritten")
	}
	if _, e = x.db.Exec(`UPDATE owned_federation_discoveries SET status='pending',result=NULL WHERE id=$1`, first.ID); e == nil {
		t.Fatal("terminal job retried")
	}
}
func TestPGFederationDiscoveryRetryLeaseAndAtomicRollback(t *testing.T) {
	x, target := discoveryFixture(t)
	trigger := "synthetic_discovery_audit_" + strings.ReplaceAll(x.actor, "-", "")
	query(t, x.db, `CREATE FUNCTION `+trigger+`()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='discovery.request' AND NEW.publisher_id='`+x.publisher+`'::uuid THEN RAISE EXCEPTION 'synthetic rollback';END IF;RETURN NEW;END$$`)
	query(t, x.db, `CREATE TRIGGER `+trigger+` BEFORE INSERT ON owned_federation_audit FOR EACH ROW EXECUTE FUNCTION `+trigger+`() `)
	_, err := x.r.RequestDiscovery(x.ctx, x.actor, x.publisher, target.Resource)
	query(t, x.db, `DROP TRIGGER `+trigger+` ON owned_federation_audit`)
	query(t, x.db, `DROP FUNCTION `+trigger+`() `)
	if err == nil {
		t.Fatal("audit failure committed")
	}
	var n int
	if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_discoveries WHERE publisher_id=$1`, x.publisher).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	job := discoveryRequest(t, x, target)
	calls := 0
	failure := discoveryFetchFunc(func(context.Context, string, string) (f.FetchResponse, error) {
		calls++
		return f.FetchResponse{Status: 503}, nil
	})
	if _, e := x.r.ProcessDiscoveries(x.ctx, failure, 1); e != nil {
		t.Fatal(e)
	}
	if discoveryState(t, x.db, job.ID) != "pending" {
		t.Fatal("retry lost")
	}
	if _, e := x.r.ProcessDiscoveries(x.ctx, failure, 1); e != nil || calls != 1 {
		t.Fatal("backoff ignored", calls, e)
	}
	// Simulated worker crash: an actual persisted expired lease is reclaimed.
	query(t, x.db, `UPDATE owned_federation_discoveries SET status='inflight',lease_token=$2,lease_until=$3 WHERE id=$1`, job.ID, authorityUUID(t), x.config.Clock().Add(-time.Second))
	if _, e := x.r.ProcessDiscoveries(x.ctx, failure, 1); e != nil || calls != 2 {
		t.Fatal("expired lease not reclaimed", e, calls)
	}
	// Attempt two backs off two minutes. Stay within the original session
	// and job deadline; advancing an hour correctly cancels stale intent.
	x.clock.Add(121)
	if _, e := x.r.ProcessDiscoveries(x.ctx, failure, 1); e != nil || calls != 3 || discoveryState(t, x.db, job.ID) != "dead" {
		t.Fatal("attempt limit", calls, e)
	}
	if _, e := x.r.ProcessDiscoveries(x.ctx, failure, 1); e != nil || calls != 3 {
		t.Fatal("dead-letter auto retry", e, calls)
	}
}
func TestPGFederationDiscoveryRechecksOwnerSessionBlockAndPins(t *testing.T) {
	_ = database(t)
	for _, change := range []string{"owner", "disable", "membership", "session", "block", "key"} {
		t.Run(change, func(t *testing.T) {
			x, target := discoveryFixture(t)
			job := discoveryRequest(t, x, target)
			response := discoveryResponse(t, x, target)
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			fetch := discoveryFetchFunc(func(ctx context.Context, u, a string) (f.FetchResponse, error) {
				reply, e := response(ctx, u, a)
				once.Do(func() { close(started); <-release })
				return reply, e
			})
			done := make(chan error, 1)
			go func() { _, e := x.r.ProcessDiscoveries(context.Background(), fetch, 1); done <- e }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("fetch did not start")
			}
			// These writes must commit while transport is paused: no SQL lock may be
			// retained across the remote fetch. Use a real deadline instead of sleeping.
			ctx, cancel := context.WithTimeout(x.ctx, 2*time.Second)
			defer cancel()
			var e error
			switch change {
			case "owner":
				other := authorityUUID(t)
				query(t, x.db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at)VALUES($1,$2,$2,clock_timestamp())`, other, "synthetic_discovery_owner_"+strings.ReplaceAll(other, "-", ""))
				_, e = x.db.ExecContext(ctx, `UPDATE owned_publishers SET account_id=$2 WHERE id=$1`, x.publisher, other)
			case "disable":
				_, e = x.db.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, x.actor)
			case "membership":
				_, e = x.db.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=clock_timestamp() WHERE publisher_id=$1 AND account_id=$2`, x.publisher, x.actor)
			case "session":
				_, e = x.db.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, x.actor)
			case "block":
				e = x.r.Change(ctx, x.actor, x.publisher, f.Change{Action: "block", RemoteActor: target.Actor})
			case "key":
				x.keys.remote.PublicKey.N.Add(x.keys.remote.PublicKey.N, big.NewInt(2))
			}
			close(release)
			if e != nil {
				t.Fatal("SQL remained locked during fetch", e)
			}
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			if state := discoveryState(t, x.db, job.ID); state != "cancelled" {
				t.Fatal(change, state)
			}
		})
	}

	t.Run("session-revoked-after-row-wait", func(t *testing.T) {
		x, target := discoveryFixture(t)
		job := discoveryRequest(t, x, target)
		response := discoveryResponse(t, x, target)
		ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
		defer cancel()
		fetched, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		fetch := discoveryFetchFunc(func(c context.Context, u, a string) (f.FetchResponse, error) {
			value, e := response(c, u, a)
			if u == target.Actor {
				close(fetched)
				select {
				case <-release:
				case <-c.Done():
					return f.FetchResponse{}, c.Err()
				}
			}
			return value, e
		})
		done := make(chan error, 1)
		go func() { _, e := x.r.ProcessDiscoveries(ctx, fetch, 1); done <- e }()
		select {
		case <-fetched:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		hold, e := x.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer hold.Rollback()
		var pid int
		if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		if _, e = hold.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, x.actor); e != nil {
			t.Fatal(e)
		}
		unblock()
		waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_authority_accounts")
		if _, e = hold.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, x.actor); e != nil {
			t.Fatal(e)
		}
		if e = hold.Commit(); e != nil {
			t.Fatal(e)
		}
		if e = <-done; e != nil {
			t.Fatal(e)
		}
		if discoveryState(t, x.db, job.ID) != "cancelled" {
			t.Fatal("pre-wait session snapshot survived")
		}
	})
}
func TestPGFederationDiscoveryConcurrentClaim(t *testing.T) {
	x, target := discoveryFixture(t)
	job := discoveryRequest(t, x, target)
	response := discoveryResponse(t, x, target)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	fetch := discoveryFetchFunc(func(ctx context.Context, u, a string) (f.FetchResponse, error) {
		calls.Add(1)
		once.Do(func() { close(started); <-release })
		return response(ctx, u, a)
	})
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessDiscoveries(context.Background(), fetch, 1); done <- e }()
	<-started
	if n, e := x.r.ProcessDiscoveries(x.ctx, fetch, 1); e != nil || n != 0 {
		t.Fatal("concurrent lease stolen", n, e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 || discoveryState(t, x.db, job.ID) != "succeeded" {
		t.Fatal(calls.Load())
	}
}
func TestPGFederationDiscoverySavedRestartPersistence(t *testing.T) {
	type savedState struct{ Publisher, Job, Rows, Server string }
	var saved savedState
	if fixtureRestartPhase(t) == "before" {
		x, target := discoveryFixture(t)
		job := discoveryRequest(t, x, target)
		if _, e := x.r.ProcessDiscoveries(x.ctx, discoveryResponse(t, x, target), 1); e != nil {
			t.Fatal(e)
		}
		server := staticDiscoveryServer(t)
		r := discoveryServerRepo(t, x, server)
		if e := r.InitializeServerIdentity(x.ctx); e != nil {
			t.Fatal(e)
		}
		saved.Publisher = x.publisher
		saved.Job = job.ID
		if e := x.db.QueryRow(`SELECT row_to_json(d)::text FROM owned_federation_discoveries d WHERE id=$1`, job.ID).Scan(&saved.Rows); e != nil {
			t.Fatal(e)
		}
		if e := x.db.QueryRow(`SELECT row_to_json(s)::text FROM owned_federation_server_identity s WHERE singleton`).Scan(&saved.Server); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, "federation-discovery-restart.json", saved)
		return
	}
	loadRestartFixture(t, "federation-discovery-restart.json", &saved)
	db := database(t)
	var rows, server string
	if e := db.QueryRow(`SELECT row_to_json(d)::text FROM owned_federation_discoveries d WHERE id=$1 AND publisher_id=$2`, saved.Job, saved.Publisher).Scan(&rows); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT row_to_json(s)::text FROM owned_federation_server_identity s WHERE singleton`).Scan(&server); e != nil {
		t.Fatal(e)
	}
	var expected, actual any
	for _, pair := range [][2]string{{saved.Rows, rows}, {saved.Server, server}} {
		if json.Unmarshal([]byte(pair[0]), &expected) != nil || json.Unmarshal([]byte(pair[1]), &actual) != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("saved durable discovery/identity changed across actual restart")
		}
	}
	if discoveryState(t, db, saved.Job) != "succeeded" {
		t.Fatal("saved result missing")
	}
	// after phase never creates actors, runs migrations, regenerates keys or jobs.
}

var _ http.Handler = federationhttp.Handler{}
