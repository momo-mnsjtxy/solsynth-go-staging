// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationhttp"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type firstContactKeys struct {
	federationKeys
	denied bool
}

func (k firstContactKeys) FirstContact(_ context.Context, candidate f.RemoteKey) (bool, error) {
	return !k.denied && k.remote.Actor != candidate.Actor && k.remote.KeyID != candidate.KeyID, nil
}
func publicAdmissionFixture(t *testing.T) (*federationFixture, f.DiscoveryTarget, f.PublicAdmissionPolicy) {
	t.Helper()
	x := newFederationFixture(t, true)
	query(t, x.db, federationpg.DiscoverySchema)
	query(t, x.db, federationpg.PublicAdmissionSchema)
	origin := "https://p" + strings.ReplaceAll(x.actor, "-", "") + ".example.test"
	x.config.AllowedOrigins = append(x.config.AllowedOrigins, origin)
	policy := f.PublicAdmissionPolicy{Origin: origin, Incarnation: authorityUUID(t)}
	x.keys.remote = f.RemoteKey{Actor: origin + "/actors/" + x.name, Inbox: origin + "/inbox", KeyID: origin + "/actors/" + x.name + "#main-key", PublicKey: &x.remoteKey.PublicKey}
	target := f.DiscoveryTarget{Resource: "acct:" + x.name + "@" + strings.TrimPrefix(origin, "https://"), Actor: x.keys.remote.Actor}
	r, e := federationpg.NewWithDiscovery(x.db, x.config, firstContactKeys{federationKeys: federationKeys{locals: x.keys.locals}}, x.guard, f.DiscoveryFeatures{})
	if e != nil {
		t.Fatal(e)
	}
	x.r, e = r.WithPublicAdmission([]f.PublicAdmissionPolicy{policy})
	if e != nil {
		t.Fatal(e)
	}
	if e = x.r.InitializePublicAdmission(x.ctx); e != nil {
		t.Fatal(e)
	}
	x.handler = federationhttp.Handler{Repository: x.r, Config: x.config, Keys: x.r.Keys(), Authority: x.verifier}
	t.Cleanup(func() {
		query(t, x.db, `UPDATE owned_federation_public_admission_jobs SET status='cancelled',lease_token=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE publisher_id=$1 AND status IN('pending','inflight')`, x.publisher)
	})
	return x, target, policy
}
func requestPublicAdmission(t *testing.T, x *federationFixture, target f.DiscoveryTarget) f.DiscoveryJob {
	t.Helper()
	job, e := x.r.RequestPublicAdmission(x.ctx, x.actor, x.publisher, target)
	if e != nil {
		t.Fatal(e)
	}
	return job
}
func publicAdmissionState(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if e := db.QueryRow(`SELECT status FROM owned_federation_public_admission_jobs WHERE id=$1`, id).Scan(&s); e != nil {
		t.Fatal(e)
	}
	return s
}
func finishPublicAdmission(t *testing.T, x *federationFixture, target f.DiscoveryTarget) f.DiscoveryJob {
	t.Helper()
	job := requestPublicAdmission(t, x, target)
	if n, e := x.r.ProcessPublicAdmissions(x.ctx, discoveryResponse(t, x, target), 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if publicAdmissionState(t, x.db, job.ID) != "succeeded" {
		t.Fatal("admission did not succeed")
	}
	return job
}
func publicAdmissionProof(t *testing.T, x *federationFixture, a f.Activity) f.Verified {
	t.Helper()
	b, _ := json.Marshal(a)
	r := httptest.NewRequest("POST", x.actorURI+"/inbox", bytes.NewReader(b))
	if e := f.Sign(r, b, x.keys.remote.KeyID, x.remoteKey, x.config.Clock()); e != nil {
		t.Fatal(e)
	}
	v, e := f.Verify(x.ctx, x.config, x.r.Keys(), r, b)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGFederationPublicAdmissionLifecycleAndIsolation(t *testing.T) {
	x, target, _ := publicAdmissionFixture(t)
	if e := x.r.PublicAdmissionReady(x.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.Keys().RemoteActor(x.ctx, target.Actor); e == nil {
		t.Fatal("unknown actor trusted")
	}
	job := finishPublicAdmission(t, x, target)
	again := requestPublicAdmission(t, x, target)
	if again.ID != job.ID {
		t.Fatal("replay reset")
	}
	k, e := x.r.Keys().RemoteActor(x.ctx, target.Actor)
	if e != nil || k.Incarnation != job.ID || k.Actor != target.Actor {
		t.Fatal(k, e)
	}
	var count int
	for _, q := range []string{`SELECT count(*) FROM owned_federation_public_audit WHERE actor_uri=$1 AND action='key.admit'`, `SELECT count(*) FROM owned_federation_audit WHERE publisher_id=$1::uuid AND action='public_admission.request'`} {
		id := target.Actor
		if strings.Contains(q, "publisher_id") {
			id = x.publisher
		}
		if e := x.db.QueryRow(q, id).Scan(&count); e != nil || count != 1 {
			t.Fatal(count, e)
		}
	}
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_authority_accounts WHERE id::text=$1 OR name=$1`, target.Actor).Scan(&count); e != nil || count != 0 {
		t.Fatal("remote became local", count, e)
	}
	if e = x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: target.Actor}); e != nil {
		t.Fatal(e)
	}
	follow := x.activity(t, "Follow", x.actorURI)
	v := publicAdmissionProof(t, x, follow)
	if e = x.r.Receive(x.ctx, x.name, v); e != nil {
		t.Fatal(e)
	}
	if e = x.r.Receive(x.ctx, x.name, v); e != nil {
		t.Fatal("idempotent inbox", e)
	}
	if _, e = x.r.RequestPublicAdmission(context.Background(), x.actor, x.publisher, f.DiscoveryTarget{Resource: target.Resource + "x", Actor: target.Actor}); e == nil {
		t.Fatal("missing proof")
	}
}
func TestPGFederationPublicAdmissionRevocationAndPermanentReservations(t *testing.T) {
	x, target, policy := publicAdmissionFixture(t)
	job := finishPublicAdmission(t, x, target)
	v := publicAdmissionProof(t, x, x.activity(t, "Follow", x.actorURI))
	if e := x.r.RevokePublicAdmission(x.ctx, policy.Origin, target.Actor); e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.Keys().RemoteActor(x.ctx, target.Actor); e == nil {
		t.Fatal("revoked key usable")
	}
	// Alternate configured IDs and aliases cannot evade the permanent actor or
	// fingerprint tombstone through the public verifier's key-ID lookup path.
	for _, disabled := range []bool{false, true} {
		for _, variant := range []string{"original", "new-key-id", "alias"} {
			pin := x.keys.remote.Snapshot()
			if variant == "alias" {
				pin.Actor += "_alias"
			}
			if variant != "original" {
				pin.KeyID = pin.Actor + "#replacement"
			}
			configured, e := federationpg.New(x.db, x.config, firstContactKeys{federationKeys: federationKeys{locals: x.keys.locals, remote: pin}}, x.guard)
			if e != nil {
				t.Fatal(e)
			}
			if !disabled {
				configured, e = configured.WithPublicAdmission([]f.PublicAdmissionPolicy{policy})
				if e != nil {
					t.Fatal(e)
				}
			}
			if _, e = configured.Keys().RemoteKey(x.ctx, pin.KeyID); !errors.Is(e, f.ErrForbidden) {
				t.Fatal("alternate pin bypassed durable key reservation", disabled, variant, e)
			}
			if _, e = configured.Keys().RemoteActor(x.ctx, pin.Actor); !errors.Is(e, f.ErrForbidden) {
				t.Fatal("static alias bypassed durable fingerprint reservation", disabled, variant, e)
			}
		}
	}
	// Disabling first-contact policy does not disable unrelated valid static
	// pins. This public-only value deliberately has no new signing credential.
	unrelated := x.keys.remote.Snapshot()
	unrelated.Actor += "_unrelated"
	unrelated.KeyID = unrelated.Actor + "#main-key"
	unrelated.PublicKey.N = new(big.Int).Add(unrelated.PublicKey.N, big.NewInt(2))
	static, e := federationpg.New(x.db, x.config, firstContactKeys{federationKeys: federationKeys{locals: x.keys.locals, remote: unrelated}}, x.guard)
	if e != nil {
		t.Fatal(e)
	}
	if got, e := static.Keys().RemoteKey(x.ctx, unrelated.KeyID); e != nil || got.Actor != unrelated.Actor || got.Incarnation != "" {
		t.Fatal("unrelated static key unavailable", got, e)
	}
	if got, e := static.Keys().RemoteActor(x.ctx, unrelated.Actor); e != nil || got.KeyID != unrelated.KeyID {
		t.Fatal("unrelated static actor unavailable", got, e)
	}
	if e := x.r.Receive(x.ctx, x.name, v); !errors.Is(e, f.ErrSignature) {
		t.Fatal("old verified proof survived", e)
	}
	for _, stmt := range []string{`UPDATE owned_federation_public_keys SET revoked_at=NULL WHERE actor_uri=$1`, `UPDATE owned_federation_public_keys SET incarnation='10000000-0000-4000-8000-000000000002' WHERE actor_uri=$1`, `DELETE FROM owned_federation_public_keys WHERE actor_uri=$1`} {
		if _, e := x.db.Exec(stmt, target.Actor); e == nil {
			t.Fatal("reservation reset", stmt)
		}
	}
	// Same original job remains terminal and never launches a refresh after revocation.
	replay := requestPublicAdmission(t, x, target)
	if replay.ID != job.ID {
		t.Fatal("new job after revocation")
	}
	calls := 0
	if n, e := x.r.ProcessPublicAdmissions(x.ctx, discoveryFetchFunc(func(context.Context, string, string) (f.FetchResponse, error) { calls++; return f.FetchResponse{}, nil }), 1); e != nil || n != 0 || calls != 0 {
		t.Fatal(n, e, calls)
	}
	alias := target
	alias.Resource = strings.Replace(alias.Resource, x.name, strings.ToUpper(x.name), 1)
	if _, e := x.r.RequestPublicAdmission(x.ctx, x.actor, x.publisher, alias); e == nil {
		t.Fatal("retired alias admitted")
	}
	if e := x.r.RevokePublicAdmission(x.ctx, policy.Origin, ""); e != nil {
		t.Fatal(e)
	}
	if e := x.r.InitializePublicAdmission(x.ctx); e == nil {
		t.Fatal("revoked policy reactivated")
	}
	for _, incarnation := range []string{policy.Incarnation, authorityUUID(t)} {
		reenabled := policy
		reenabled.Incarnation = incarnation
		repo, e := static.WithPublicAdmission([]f.PublicAdmissionPolicy{reenabled})
		if e != nil {
			t.Fatal(e)
		}
		if e = repo.InitializePublicAdmission(x.ctx); e == nil {
			t.Fatal("re-added configuration revived original policy", incarnation)
		}
		if _, e = repo.Keys().RemoteActor(x.ctx, target.Actor); !errors.Is(e, f.ErrForbidden) {
			t.Fatal("re-added configuration revived original identity", e)
		}
	}
}
func TestPGFederationPublicAdmissionAuthorityABAAndOriginalDeadline(t *testing.T) {
	_ = database(t)
	for _, change := range []string{"member", "permission", "session", "block", "expiry", "policy"} {
		t.Run(change, func(t *testing.T) {
			x, target, policy := publicAdmissionFixture(t)
			job := requestPublicAdmission(t, x, target)
			response := discoveryResponse(t, x, target)
			calls := 0
			fetch := discoveryFetchFunc(func(c context.Context, u, a string) (f.FetchResponse, error) {
				calls++
				v, e := response(c, u, a)
				if calls == 1 {
					switch change {
					case "member":
						query(t, x.db, `UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1 AND account_id=$2`, x.publisher, x.actor)
						query(t, x.db, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1 AND account_id=$2`, x.publisher, x.actor)
					case "permission":
						store := authoritypg.New(x.db)
						if e := store.ReplacePolicy(x.ctx, x.actor, nil, nil); e != nil {
							t.Fatal(e)
						}
						if e := store.ReplacePolicy(x.ctx, x.actor, []authority.Rule{{Pattern: "posts.*"}, {Pattern: "publishers.*"}}, nil); e != nil {
							t.Fatal(e)
						}
					case "session":
						query(t, x.db, `UPDATE owned_authority_sessions SET scopes='["posts.view"]'::jsonb WHERE account_id=$1`, x.actor)
						query(t, x.db, `UPDATE owned_authority_sessions SET scopes='["*"]'::jsonb WHERE account_id=$1`, x.actor)
					case "block":
						query(t, x.db, `INSERT INTO owned_federation_peers(actor_uri,inbox_uri,updated_at)VALUES($1,$2,clock_timestamp())`, target.Actor, x.keys.remote.Inbox)
						query(t, x.db, `INSERT INTO owned_federation_blocks(publisher_id,actor_uri,created_at)VALUES($1,$2,clock_timestamp())`, x.publisher, target.Actor)
						query(t, x.db, `DELETE FROM owned_federation_blocks WHERE publisher_id=$1 AND actor_uri=$2`, x.publisher, target.Actor)
					case "expiry":
						query(t, x.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, x.actor)
					case "policy":
						if e := x.r.RevokePublicAdmission(x.ctx, policy.Origin, ""); e != nil {
							t.Fatal(e)
						}
					}
				}
				return v, e
			})
			if _, e := x.r.ProcessPublicAdmissions(x.ctx, fetch, 1); e != nil {
				t.Fatal(e)
			}
			if publicAdmissionState(t, x.db, job.ID) != "cancelled" || calls != 1 {
				t.Fatal(change, calls, publicAdmissionState(t, x.db, job.ID))
			}
			var count int
			if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_public_keys WHERE actor_uri=$1`, target.Actor).Scan(&count); e != nil || count != 0 {
				t.Fatal(count, e)
			}
		})
	}
}
func TestPGFederationPublicAdmissionRetryLeaseAndLateResults(t *testing.T) {
	x, target, _ := publicAdmissionFixture(t)
	job := requestPublicAdmission(t, x, target)
	retry := discoveryFetchFunc(func(context.Context, string, string) (f.FetchResponse, error) {
		return f.FetchResponse{Status: 503}, nil
	})
	if _, e := x.r.ProcessPublicAdmissions(x.ctx, retry, 1); e != nil {
		t.Fatal(e)
	}
	if publicAdmissionState(t, x.db, job.ID) != "pending" {
		t.Fatal("no retry")
	}
	x.clock.Add(120)
	response := discoveryResponse(t, x, target)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fetch := discoveryFetchFunc(func(c context.Context, u, a string) (f.FetchResponse, error) {
		once.Do(func() { close(started); <-release })
		return response(c, u, a)
	})
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessPublicAdmissions(context.Background(), fetch, 1); done <- e }()
	<-started
	// Expire original lease using the injected clock; a second worker may reclaim.
	x.clock.Add(20)
	if n, e := x.r.ProcessPublicAdmissions(x.ctx, response, 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if publicAdmissionState(t, x.db, job.ID) != "succeeded" {
		t.Fatal("late lease overwrote winner")
	}
	var count int
	if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_public_audit WHERE job_id=$1 AND action='key.admit'`, job.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestPGFederationPublicAdmissionConcurrentClaimsAndCollision(t *testing.T) {
	x, target, _ := publicAdmissionFixture(t)
	job := requestPublicAdmission(t, x, target)
	response := discoveryResponse(t, x, target)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	fetch := discoveryFetchFunc(func(c context.Context, u, a string) (f.FetchResponse, error) {
		calls.Add(1)
		once.Do(func() { close(started); <-release })
		return response(c, u, a)
	})
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessPublicAdmissions(context.Background(), fetch, 1); done <- e }()
	<-started
	if n, e := x.r.ProcessPublicAdmissions(x.ctx, response, 1); e != nil || n != 0 {
		t.Fatal("claim stolen", n, e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 || publicAdmissionState(t, x.db, job.ID) != "succeeded" {
		t.Fatal(calls.Load())
	}
	// A distinct actor reusing the enrolled public key cannot capture that key.
	target2 := target
	target2.Actor += "_other"
	target2.Resource = strings.Replace(target.Resource, x.name, x.name+"_other", 1)
	x.name += "_other"
	x.keys.remote.Actor = target2.Actor
	x.keys.remote.KeyID = target2.Actor + "#main-key"
	second := requestPublicAdmission(t, x, target2)
	if _, e := x.r.ProcessPublicAdmissions(x.ctx, discoveryResponse(t, x, target2), 1); e != nil {
		t.Fatal(e)
	}
	if publicAdmissionState(t, x.db, second.ID) != "dead" {
		t.Fatal("shared fingerprint admitted")
	}
}
func TestPGFederationPublicAdmissionAtomicAuditRollback(t *testing.T) {
	x, target, _ := publicAdmissionFixture(t)
	job := requestPublicAdmission(t, x, target)
	trigger := "synthetic_public_audit_" + strings.ReplaceAll(x.actor, "-", "")
	query(t, x.db, `CREATE FUNCTION `+trigger+`()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='key.admit' AND NEW.actor_uri='`+target.Actor+`' THEN RAISE EXCEPTION 'synthetic rollback';END IF;RETURN NEW;END$$`)
	query(t, x.db, `CREATE TRIGGER `+trigger+` BEFORE INSERT ON owned_federation_public_audit FOR EACH ROW EXECUTE FUNCTION `+trigger+`() `)
	_, err := x.r.ProcessPublicAdmissions(x.ctx, discoveryResponse(t, x, target), 1)
	query(t, x.db, `DROP TRIGGER `+trigger+` ON owned_federation_public_audit`)
	query(t, x.db, `DROP FUNCTION `+trigger+`() `)
	if err == nil {
		t.Fatal("failed audit committed")
	}
	var count int
	if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_public_keys WHERE actor_uri=$1`, target.Actor).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if publicAdmissionState(t, x.db, job.ID) != "inflight" {
		t.Fatal("lease not recoverable")
	}
	x.clock.Add(20)
	if _, e := x.r.ProcessPublicAdmissions(x.ctx, discoveryResponse(t, x, target), 1); e != nil {
		t.Fatal(e)
	}
	if publicAdmissionState(t, x.db, job.ID) != "succeeded" {
		t.Fatal("rollback unrecoverable")
	}
}
func TestPGFederationPublicAdmissionSavedRestartPersistence(t *testing.T) {
	type savedState struct {
		Origin                                                       string
		AllowedOrigins                                               []string
		MaxAttempts                                                  int
		Timeout                                                      int64
		Policy                                                       f.PublicAdmissionPolicy
		Actor, Job, KeyRow, JobRow, PolicyRow, SessionRow, SessionID string
		RequestURL                                                   string
		RequestBody                                                  []byte
		RequestHeaders                                               http.Header
		SignedAt                                                     time.Time
		CapturedWall                                                 time.Time
	}
	var saved savedState
	if fixtureRestartPhase(t) == "before" {
		x, target, policy := publicAdmissionFixture(t)
		job := finishPublicAdmission(t, x, target)
		saved.Origin = x.config.Origin
		saved.AllowedOrigins = x.config.AllowedOrigins
		saved.MaxAttempts = x.config.MaxAttempts
		saved.Timeout = int64(x.config.DeliveryTimeout)
		saved.Policy = policy
		saved.Actor = target.Actor
		saved.Job = job.ID
		saved.RequestURL = x.actorURI + "/inbox"
		saved.SignedAt = x.config.Clock()
		saved.CapturedWall = time.Now().UTC()
		body, e := json.Marshal(x.activity(t, "Follow", x.actorURI))
		if e != nil {
			t.Fatal(e)
		}
		saved.RequestBody = body
		request := httptest.NewRequest("POST", saved.RequestURL, bytes.NewReader(saved.RequestBody))
		if e = f.Sign(request, saved.RequestBody, x.keys.remote.KeyID, x.remoteKey, saved.SignedAt); e != nil {
			t.Fatal(e)
		}
		saved.RequestHeaders = request.Header.Clone()
		proof, e := f.Verify(x.ctx, x.config, x.r.Keys(), request, saved.RequestBody)
		if e != nil || proof.Remote().Incarnation != job.ID {
			t.Fatal("original public proof unavailable", e)
		}
		if e := x.db.QueryRow(`SELECT row_to_json(k)::text FROM owned_federation_public_keys k WHERE actor_uri=$1`, target.Actor).Scan(&saved.KeyRow); e != nil {
			t.Fatal(e)
		}
		if e := x.db.QueryRow(`SELECT row_to_json(j)::text,requester_session::text FROM owned_federation_public_admission_jobs j WHERE id=$1`, job.ID).Scan(&saved.JobRow, &saved.SessionID); e != nil {
			t.Fatal(e)
		}
		if e := x.db.QueryRow(`SELECT row_to_json(p)::text FROM owned_federation_public_policies p WHERE origin=$1`, policy.Origin).Scan(&saved.PolicyRow); e != nil {
			t.Fatal(e)
		}
		if e := x.db.QueryRow(`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, saved.SessionID).Scan(&saved.SessionRow); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, "federation-public-admission-restart.json", saved)
		return
	}
	loadRestartFixture(t, "federation-public-admission-restart.json", &saved)
	db := database(t)
	for _, pair := range []struct{ stmt, id, want string }{{`SELECT row_to_json(k)::text FROM owned_federation_public_keys k WHERE actor_uri=$1`, saved.Actor, saved.KeyRow}, {`SELECT row_to_json(j)::text FROM owned_federation_public_admission_jobs j WHERE id=$1`, saved.Job, saved.JobRow}, {`SELECT row_to_json(p)::text FROM owned_federation_public_policies p WHERE origin=$1`, saved.Policy.Origin, saved.PolicyRow}, {`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, saved.SessionID, saved.SessionRow}} {
		var got string
		if e := db.QueryRow(pair.stmt, pair.id).Scan(&got); e != nil {
			t.Fatal(e)
		}
		var expected, actual any
		if json.Unmarshal([]byte(pair.want), &expected) != nil || json.Unmarshal([]byte(got), &actual) != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("original durable state changed")
		}
	}
	config := f.Config{Origin: saved.Origin, AllowedOrigins: saved.AllowedOrigins, MaxAttempts: saved.MaxAttempts, DeliveryTimeout: time.Duration(saved.Timeout), Now: func() time.Time { return saved.SignedAt }}
	r, e := federationpg.New(db, config, firstContactKeys{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	r, e = r.WithPublicAdmission([]f.PublicAdmissionPolicy{saved.Policy})
	if e != nil {
		t.Fatal(e)
	}
	k, e := r.Keys().RemoteActor(context.Background(), saved.Actor)
	if e != nil || k.Incarnation != saved.Job {
		t.Fatal("fresh process lost original key", k, e)
	}
	request := httptest.NewRequest("POST", saved.RequestURL, bytes.NewReader(saved.RequestBody))
	request.Header = saved.RequestHeaders.Clone()
	// Signing-time verification below proves original public-key bytes persisted,
	// not that an aged request acquires a fresh dispatch authorization.
	proof, e := f.Verify(context.Background(), config, r.Keys(), request, saved.RequestBody)
	if e != nil || proof.Remote().Incarnation != saved.Job || !proof.ValidFor(strings.TrimSuffix(saved.RequestURL, "/inbox")) {
		t.Fatal("original signed proof failed after restart", e)
	}
	elapsedConfig := config
	elapsedConfig.Now = func() time.Time { return saved.SignedAt.Add(time.Since(saved.CapturedWall)) }
	_, elapsedErr := f.Verify(context.Background(), elapsedConfig, r.Keys(), request, saved.RequestBody)
	if time.Since(saved.CapturedWall) > 5*time.Minute && elapsedErr == nil {
		t.Fatal("restart renewed expired signed request")
	}
	expiredConfig := config
	expiredConfig.Now = func() time.Time { return saved.SignedAt.Add(6 * time.Minute) }
	if _, err := f.Verify(context.Background(), expiredConfig, r.Keys(), request, saved.RequestBody); err == nil {
		t.Fatal("original proof deadline extended")
	}

	request.Header.Set("Signature", strings.Replace(request.Header.Get("Signature"), "#main-key", "#unadmitted", 1))
	if _, e = f.Verify(context.Background(), config, r.Keys(), request, saved.RequestBody); !errors.Is(e, f.ErrSignature) {
		t.Fatal("altered proof accepted after restart", e)
	}
	// No migration, seed, token issuance, key generation or new marker after restart.
}
