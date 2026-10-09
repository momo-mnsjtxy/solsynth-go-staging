// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

// The disposable marker stores public keys, the ORIGINAL synthetic request and
// public-only authority verifier. No private signing material is persisted.
// The marker is mode 0600 and is never a CI artifact.
// After restart there is no issuer, key generation, session or target recreation.
func TestPGFederationDeliverySavedRestartPersistence(t *testing.T) {
	type savedState struct {
		Authority                             restartAuthoritySnapshot
		Token, Actor, Publisher, Session      string
		Delivery                              int64
		DeliveryRow, SessionRow, PublisherRow string
		Origin                                string
		Origins                               []string
		Remote                                f.RemoteKey
		LocalPublic                           f.RemoteKey
		RequestURL                            string
		RequestBody                           []byte
		RequestHeaders                        http.Header
		SignedAt                              time.Time
	}
	var saved savedState
	if fixtureRestartPhase(t) == "before" {
		x := newFederationFixtureWithAuthority(t, true, func(c *authority.Config) {
			initial, wall := c.Now().UTC(), time.Now()
			c.AccessTTL = time.Hour
			c.SessionTTL = time.Hour
			c.Now = func() time.Time { return initial.Add(time.Since(wall)) }
		})
		x.preserve = true
		saved.Authority = saveAuthoritySnapshot(t, x.authConfig)
		saved.Token, saved.Actor, saved.Publisher = x.token, x.actor, x.publisher
		saved.Delivery = queueDeliveryFollow(t, x)
		// Capture the exact signed request, then simulate a worker losing its
		// context after transmission but before durable acknowledgement.
		ctx, cancel := context.WithCancel(context.Background())
		transport := deliverySendFunc(func(_ context.Context, request *http.Request) (int, error) {
			var err error
			saved.RequestBody, err = io.ReadAll(request.Body)
			saved.RequestURL = request.URL.String()
			saved.RequestHeaders = request.Header.Clone()
			saved.SignedAt, _ = http.ParseTime(request.Header.Get("Date"))
			cancel()
			return 202, err
		})
		if n, e := x.r.ProcessDeliveries(ctx, transport, 1); e != nil || n != 1 {
			cancel()
			t.Fatal(n, e)
		}
		cancel()
		query(t, x.db, `UPDATE owned_federation_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, saved.Delivery)

		_, _, saved.DeliveryRow = deliveryRow(t, x.db, saved.Delivery)
		if e := x.db.QueryRow(`SELECT origin_intent->>'session' FROM owned_federation_deliveries WHERE id=$1`, saved.Delivery).Scan(&saved.Session); e != nil {
			t.Fatal(e)
		}
		if e := x.db.QueryRow(`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, saved.Session).Scan(&saved.SessionRow); e != nil {
			t.Fatal(e)
		}
		if e := x.db.QueryRow(`SELECT row_to_json(p)::text FROM owned_federation_publishers p WHERE publisher_id=$1`, x.publisher).Scan(&saved.PublisherRow); e != nil {
			t.Fatal(e)
		}
		saved.Origin, saved.Origins, saved.Remote = x.config.Origin, x.config.AllowedOrigins, x.keys.remote
		saved.LocalPublic = f.RemoteKey{Actor: x.actorURI, Inbox: x.actorURI + "/inbox", KeyID: x.actorURI + "#main-key", PublicKey: &x.keys.locals[x.publisher].PublicKey}
		saveRestartFixture(t, "federation-delivery-restart.json", saved)
		return
	}
	loadRestartFixture(t, "federation-delivery-restart.json", &saved)
	db := database(t)
	for _, item := range []struct {
		statement string
		arg       any
		want      string
	}{
		{`SELECT row_to_json(d)::text FROM owned_federation_deliveries d WHERE id=$1`, saved.Delivery, saved.DeliveryRow},
		{`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, saved.Session, saved.SessionRow},
		{`SELECT row_to_json(p)::text FROM owned_federation_publishers p WHERE publisher_id=$1`, saved.Publisher, saved.PublisherRow},
	} {
		var got string
		var expected, actual any
		if e := db.QueryRow(item.statement, item.arg).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if json.Unmarshal([]byte(got), &actual) != nil || json.Unmarshal([]byte(item.want), &expected) != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal("original persisted row changed")
		}
	}
	config := restoreAuthoritySnapshot(t, saved.Authority)
	verifier, e := authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = verifier.Authenticate(context.Background(), saved.Token); e != nil {
		t.Fatal("original bearer unavailable", e)
	}
	// Signing-time verification proves persistence of the original signature;
	// it is separate from current-clock admission and never authorizes a send.
	peerOrigin, _ := f.Origin(saved.Remote.Actor)
	signatureConfig := f.Config{Origin: peerOrigin, AllowedOrigins: []string{saved.Origin}, MaxAttempts: 3, DeliveryTimeout: time.Second, Now: func() time.Time { return saved.SignedAt }}
	request := httptest.NewRequest("POST", saved.RequestURL, bytes.NewReader(saved.RequestBody))
	request.Header = saved.RequestHeaders.Clone()
	publicKeys := federationKeys{remote: saved.LocalPublic}
	if _, e = f.Verify(context.Background(), signatureConfig, publicKeys, request, saved.RequestBody); e != nil {
		t.Fatal("original public signature changed", e)
	}
	signatureConfig.Now = config.Now
	_, currentError := f.Verify(context.Background(), signatureConfig, publicKeys, request, saved.RequestBody)
	if config.Now().Sub(saved.SignedAt) > 5*time.Minute {
		if currentError == nil {
			t.Fatal("expired request admitted at elapsed clock")
		}
	} else if currentError != nil {
		t.Fatal(currentError)
	}
	signatureConfig.Now = func() time.Time { return saved.SignedAt.Add(6 * time.Minute) }
	if _, e = f.Verify(context.Background(), signatureConfig, publicKeys, request, saved.RequestBody); e == nil {
		t.Fatal("six-minute signature was admitted")
	}
	// An operator's original private key loader is deliberately unavailable in
	// this fresh process. Reclamation must fail closed, never mint another key.
	keys := federationKeys{remote: saved.Remote}

	policy := f.Config{Origin: saved.Origin, AllowedOrigins: saved.Origins, Now: config.Now, MaxAttempts: 12, DeliveryTimeout: 5 * time.Second}
	r, e := federationpg.New(db, policy, keys, authoritypg.NewTransactionGuard(authoritypg.New(db), verifier))
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	capture := &federationCapture{}
	if n, e := r.ProcessDeliveries(context.Background(), capture, 1); e != nil || n != 1 {
		t.Fatal("original leased delivery not recovered", n, e)
	}
	state, attempts, _ := deliveryRow(t, db, saved.Delivery)
	if state != "dead" || attempts != 2 || len(capture.requests) != 0 {
		t.Fatal(state, attempts, len(capture.requests))
	}
	var original struct {
		Activity string `json:"activity_id"`
	}
	if json.Unmarshal([]byte(saved.DeliveryRow), &original) != nil {
		t.Fatal("bad marker")
	}
	var activity f.Activity
	if json.Unmarshal(saved.RequestBody, &activity) != nil || activity.ID != original.Activity {
		t.Fatal("restart replaced activity identity")
	}
	// No session refresh or credential issue is hidden in recovery.
	var after string
	if e = db.QueryRow(`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, saved.Session).Scan(&after); e != nil || after != saved.SessionRow {
		t.Fatal("recovery altered original session", e)
	}
}
