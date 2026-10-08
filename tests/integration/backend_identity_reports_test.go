// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"net/url"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/passport/progression"
	"testing"
)

func TestPGBackendOAuthComposition(t *testing.T) {
	f := newOAuthFixture(t, false)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: authorityConfig(t, f.keyDir, f.clock), SessionService: f.issuer, PublicVisibility: &visibility, MessagerPolicy: &policy, OAuthEnabled: true, OAuthConsentURL: "https://synthetic-consent.invalid/authorize"})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	oauthHTTP(t, app, "GET", "/.well-known/openid-configuration", "", nil, 200)
	req, verifier := oauthRequest(f)
	form := oauthForm(req)
	oauthHTTP(t, app, "GET", "/stargate/auth/open/authorize?"+form.Encode(), f.userToken, nil, 200)
	form.Set("authorize", "true")
	decision := backendDecode[oauth.Decision](t, oauthHTTP(t, app, "POST", "/stargate/auth/open/authorize", f.userToken, form, 200))
	redirect, e := url.Parse(decision.RedirectURI)
	if e != nil {
		t.Fatal(e)
	}
	tokens := backendDecode[oauth.Tokens](t, oauthHTTP(t, app, "POST", "/stargate/auth/open/token", "", url.Values{"grant_type": {"authorization_code"}, "client_id": {req.ClientID}, "redirect_uri": {req.RedirectURI}, "code": {redirect.Query().Get("code")}, "code_verifier": {verifier}}, 200))
	claims := backendDecode[map[string]any](t, oauthHTTP(t, app, "GET", "/auth/open/userinfo", tokens.AccessToken, nil, 200))
	if claims["sub"] != f.subject {
		t.Fatal("scoped subject projection")
	}
	backendCall(t, app, "GET", "/stargate/accounts/me", tokens.AccessToken, "", 401)
	backendCall(t, app, "DELETE", "/stargate/sessions/current", f.userToken, "", 204)
	oauthHTTP(t, app, "GET", "/auth/open/userinfo", tokens.AccessToken, nil, 401)
}
func TestPGBackendFortuneComposition(t *testing.T) {
	f := newBackendFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Fortunes: &fortunepg.Config{}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET language='en' WHERE id=$1::uuid`, f.actor)
	if e = app.InstallFortuneCorpus(context.Background(), fortuneCorpus("composed-"+f.actor)); e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "GET", "/passport/fortune?language=en", f.token, "", 200)
	backendCall(t, app, "GET", "/passport/fortune?language=en", "", "", 401)
	v := backendDecode[progression.CheckIn](t, backendCall(t, app, "POST", "/passport/accounts/me/check-in?version=2", f.token, "", 200))
	if v.FortuneReport != nil {
		t.Fatal("report completed before worker")
	}
	for i := 0; i < 100; i++ {
		done, e := app.ProcessFortune(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if !done {
			break
		}
	}
	got := backendDecode[progression.CheckIn](t, backendCall(t, app, "GET", "/passport/accounts/me/check-in?version=2", f.token, "", 200))
	if got.ID != v.ID || got.FortuneReport == nil {
		t.Fatal("durable report projection", got)
	}
	backendCall(t, app, "GET", "/passport/accounts/me/check-in/history", f.token, "", 200)
}
