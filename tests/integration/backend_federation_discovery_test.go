// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"net/url"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/messagerpg"
	f "solsynth.local/go-migration/ports/federation"
	"testing"
)

func TestPGBackendFederationDiscoveryComposition(t *testing.T) {
	x := newRenameFixture(t, true)
	query(t, x.db, federationpg.DiscoverySchema)
	server := staticDiscoveryServer(t)
	target := f.DiscoveryTarget{Resource: "acct:syntheticremote@remote.example.test", Actor: x.keys.remote.Actor}
	features := f.DiscoveryFeatures{Server: &server, Targets: []f.DiscoveryTarget{target}}
	repo, e := federationpg.NewWithDiscovery(x.db, x.policy, x.keys, x.guard, features)
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.InitializeServerIdentity(x.ctx); e != nil {
		t.Fatal(e)
	}
	public, e := f.PEM(x.keys.signer)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	fetch := discoveryFetchFunc(func(ctx context.Context, uri, accept string) (f.FetchResponse, error) {
		calls++
		var value any
		if uri == target.WebFingerURL() {
			value = map[string]any{"subject": target.Resource, "links": []any{map[string]string{"rel": "self", "type": f.MediaType, "href": target.Actor}}}
		} else if uri == target.Actor {
			value = map[string]any{"id": target.Actor, "type": "Person", "preferredUsername": "syntheticremote", "name": "Synthetic remote", "inbox": x.keys.remote.Inbox, "publicKey": f.PublicKey{ID: x.keys.remote.KeyID, Owner: target.Actor, PEM: public}}
		} else {
			return f.FetchResponse{}, f.ErrForbidden
		}
		body, _ := json.Marshal(value)
		return f.FetchResponse{Status: 200, ContentType: accept, Body: body}, nil
	})
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	config := application.Config{Authority: x.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Federation: &application.FederationConfig{Policy: x.policy, Keys: x.keys, Discovery: features, Fetcher: fetch}}
	app, e := application.NewPostgres(x.db, nil, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(x.ctx); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/.well-known/host-meta", "/activitypub/actor", "/activitypub/actor/main-key", "/.well-known/webfinger?resource=acct:" + server.Username + "@local.example.test"} {
		backendCall(t, app, "GET", path, "", "", 200)
	}
	path := "/api/activitypub/publishers/" + x.publisher + "/discovery"
	backendCall(t, app, "POST", path, "", `{"resource":"`+target.Resource+`"}`, 401)
	initial := backendDecode[f.DiscoveryJob](t, backendCall(t, app, "POST", path, x.token, `{"resource":"`+target.Resource+`"}`, 200))
	if initial.State != "pending" || calls != 0 {
		t.Fatal("HTTP request fetched synchronously", initial, calls)
	}
	// Network requires both configured targets and explicit worker transport.
	config.Federation.Fetcher = nil
	offline, e := application.NewPostgres(x.db, nil, config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = offline.ProcessExternalDue(x.ctx, 1); e != nil || calls != 0 {
		t.Fatal("discovery unexpectedly activated", e, calls)
	}
	if _, e = app.ProcessExternalDue(x.ctx, 1); e != nil || calls != 2 {
		t.Fatal("configured worker", e, calls)
	}
	result := backendDecode[f.DiscoveryJob](t, backendCall(t, app, "GET", path+"?resource="+url.QueryEscape(target.Resource), x.token, "", 200))
	if result.ID != initial.ID || result.State != "succeeded" {
		t.Fatal(result)
	}
	if _, e = application.NewPostgres(x.db, database(t), config); e == nil {
		t.Fatal("separate authority pool accepted")
	}
}
