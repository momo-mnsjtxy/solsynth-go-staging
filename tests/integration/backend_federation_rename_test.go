// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	federation "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/namecards"
	"testing"
)

func TestPGBackendFederatedRenameComposition(t *testing.T) {
	f := newRenameFixture(t, true)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, NameCards: &f.options, Federation: &application.FederationConfig{Policy: f.policy, Keys: f.keys}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	before := backendDecode[federation.Actor](t, backendCall(t, app, "GET", "/activitypub/actors/"+f.oldName, "", "", 200))
	paidNameCard(t, f.nameCardFixture, "composed-alias-order")
	name := "composed_" + f.publisher
	backendJSONCall(t, app, "POST", "/stargate/accounts/me/name-change-card/use", f.token, namecards.UseRequest{Target: "publisher", TargetID: &f.publisher, NewName: name}, 200)
	for _, handle := range []string{f.oldName, name} {
		actor := backendDecode[federation.Actor](t, backendCall(t, app, "GET", "/activitypub/actors/"+handle, "", "", 200))
		if actor.ID != before.ID || actor.PublicKey != before.PublicKey || actor.Username != name {
			t.Fatal("cached actor identity changed")
		}
		pub := backendDecode[accounts.Publisher](t, backendCall(t, app, "GET", "/sphere/publishers/"+handle, "", "", 200))
		if pub.ID != f.publisher || pub.Name != name {
			t.Fatal("canonical publisher alias lost")
		}
	}
	backendJSONCall(t, app, "PATCH", "/sphere/publishers/"+f.oldName, f.token, map[string]string{"nick": "Synthetic presentation update"}, 200)
	actor := backendDecode[federation.Actor](t, backendCall(t, app, "GET", "/activitypub/actors/"+name, "", "", 200))
	if actor.ID != before.ID || actor.PublicKey != before.PublicKey || actor.Name != "Synthetic presentation update" {
		t.Fatal("display name changed identity")
	}
}
