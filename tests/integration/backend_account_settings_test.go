// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
	"testing"
)

func TestPGBackendAccountSettingsComposition(t *testing.T) {
	f := newSettingsFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor.ID, []authority.Rule{{Pattern: "accounts.manage"}, {Pattern: "account.contacts.manage"}, {Pattern: "notifications.preferences.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: authorityConfig(t, f.keys, f.now), SessionService: f.issuer, Signin: &f.config, PublicVisibility: &visibility, MessagerPolicy: &policy})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	own := backendDecode[accounts.Account](t, backendJSONCall(t, app, "PATCH", "/stargate/accounts/me/profile", f.token, map[string]any{"first_name": "Synthetic profile", "birthday": "2000-01-02T00:00:00Z"}, 200))
	if own.Profile == nil || own.Profile.Birthday == nil {
		t.Fatal("owner birthday projection")
	}
	public := backendCall(t, app, "GET", "/stargate/accounts/"+f.actor.Name, "", "", 200)
	if strings.Contains(public.Body.String(), "birthday") {
		t.Fatal("public birthday leak")
	}
	email := "synthetic_composed_" + strings.ReplaceAll(f.actor.ID, "-", "") + "@example.invalid"
	contact := backendDecode[signin.Contact](t, backendJSONCall(t, app, "POST", "/stargate/contacts", f.token, signin.ContactInput{Type: 0, Content: email}, 201))
	backendCall(t, app, "POST", "/stargate/contacts/"+contact.ID+"/verify", f.token, "", 202)
	code := f.delivery.code(email, "verify_contact")
	if code == "" {
		t.Fatal("synthetic verification delivery missing")
	}
	backendJSONCall(t, app, "POST", "/stargate/accounts/verify", "", map[string]string{"code": code}, 204)
	backendCall(t, app, "POST", "/stargate/contacts/"+contact.ID+"/public", f.token, "", 200)
	public = backendCall(t, app, "GET", "/stargate/accounts/"+f.actor.Name, "", "", 200)
	if !strings.Contains(public.Body.String(), email) {
		t.Fatal("verified public contact omitted")
	}
	backendJSONCall(t, app, "PUT", "/passport/accounts/me/notification-preferences/chat", f.token, map[string]string{"preference": "silent"}, 200)
	backendCall(t, app, "GET", "/api/accounts/me/notification-preferences", f.token, "", 200)
	backendCall(t, app, "POST", "/stargate/contacts/"+contact.ID+"/primary", f.token, "", 200)
	backendCall(t, app, "GET", "/stargate/accounts/me", f.token, "", 401)
}
