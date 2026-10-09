// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/messagerpg"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
	"testing"
)

type publicAdmissionHostKeys struct{ f.KeyProvider }

func (k publicAdmissionHostKeys) FirstContact(ctx context.Context, candidate f.RemoteKey) (bool, error) {
	_, ae := k.RemoteActor(ctx, candidate.Actor)
	_, ke := k.RemoteKey(ctx, candidate.KeyID)
	return errors.Is(ae, f.ErrNotFound) && errors.Is(ke, f.ErrNotFound), nil
}
func TestPGBackendFederationPublicAdmissionComposition(t *testing.T) {
	x := newRenameFixture(t, true)
	query(t, x.db, federationpg.DiscoverySchema)
	query(t, x.db, federationpg.PublicAdmissionSchema)
	origin := "https://h" + strings.ReplaceAll(x.actor, "-", "") + ".example.test"
	x.policy.AllowedOrigins = append(x.policy.AllowedOrigins, origin)
	policy := f.PublicAdmissionPolicy{Origin: origin, Incarnation: authorityUUID(t)}
	keys := publicAdmissionHostKeys{x.keys}
	repo, e := federationpg.NewWithDiscovery(x.db, x.policy, keys, x.guard, f.DiscoveryFeatures{})
	if e != nil {
		t.Fatal(e)
	}
	repo, e = repo.WithPublicAdmission([]f.PublicAdmissionPolicy{policy})
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.InitializePublicAdmission(x.ctx); e != nil {
		t.Fatal(e)
	}
	target := f.DiscoveryTarget{Resource: "acct:syntheticremote@" + strings.TrimPrefix(origin, "https://"), Actor: origin + "/actors/syntheticremote"}
	public := staticDiscoveryServer(t).PublicKeyPEM
	calls := 0
	fetch := discoveryFetchFunc(func(c context.Context, u, a string) (f.FetchResponse, error) {
		calls++
		var value any
		if u == target.WebFingerURL() {
			value = map[string]any{"subject": target.Resource, "links": []any{map[string]string{"rel": "self", "type": f.MediaType, "href": target.Actor}}}
		} else if u == target.Actor {
			value = map[string]any{"id": target.Actor, "type": "Person", "preferredUsername": "syntheticremote", "inbox": origin + "/inbox", "publicKey": f.PublicKey{ID: target.Actor + "#main-key", Owner: target.Actor, PEM: public}}
		} else {
			return f.FetchResponse{}, f.ErrForbidden
		}
		body, _ := json.Marshal(value)
		return f.FetchResponse{Status: 200, ContentType: a, Body: body}, nil
	})
	visibility := int32(0)
	mp := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	config := application.Config{Authority: x.config, PublicVisibility: &visibility, MessagerPolicy: &mp, Federation: &application.FederationConfig{Policy: x.policy, Keys: keys, PublicAdmission: []f.PublicAdmissionPolicy{policy}, AdmissionFetcher: fetch}}
	app, e := application.NewPostgres(x.db, nil, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(x.ctx); e != nil {
		t.Fatal(e)
	}
	path := "/api/activitypub/publishers/" + x.publisher + "/public-admissions"
	body, _ := json.Marshal(target)
	backendCall(t, app, "POST", path, "", string(body), 401)
	job := backendDecode[f.DiscoveryJob](t, backendCall(t, app, "POST", path, x.token, string(body), 200))
	if calls != 0 || job.State != "pending" {
		t.Fatal("synchronous fetch", calls, job)
	}
	config.Federation.AdmissionFetcher = nil
	offline, e := application.NewPostgres(x.db, nil, config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = offline.ProcessExternalDue(x.ctx, 1); e != nil || calls != 0 {
		t.Fatal("implicit network", e, calls)
	}
	if _, e = app.ProcessExternalDue(x.ctx, 1); e != nil || calls != 2 {
		t.Fatal(e, calls)
	}
	result := backendDecode[f.DiscoveryJob](t, backendCall(t, app, "GET", path+"?resource="+url.QueryEscape(target.Resource)+"&actor="+url.QueryEscape(target.Actor), x.token, "", 200))
	if result.State != "succeeded" || result.ID != job.ID {
		t.Fatal(result)
	}
	if _, e = application.NewPostgres(x.db, database(t), config); e == nil {
		t.Fatal("separate pool")
	}
	config.Federation.PublicAdmission = nil
	config.Federation.AdmissionFetcher = fetch
	if _, e = application.NewPostgres(x.db, nil, config); e == nil {
		t.Fatal("fetcher without policy")
	}
}
