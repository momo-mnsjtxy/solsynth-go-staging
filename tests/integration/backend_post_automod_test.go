// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	c "solsynth.local/go-migration/ports/admin/content"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
)

func TestPGBackendPostAutomodCompositionAndExplicitReview(t *testing.T) {
	f := automodFixture(t, false)
	token := "composed-auto-" + authorityUUID(t)
	automodRule(t, f, a.Hide, token)
	app := backendApp(t, f.db, f.config)
	p := backendDecode[posts.Post](t, backendJSONCall(t, app, "POST", "/sphere/posts?pub="+f.individual.Name, f.token, map[string]any{"content": token}, 200))
	if p.Automod == nil || !p.Automod.Hidden {
		t.Fatal("host post did not evaluate rules", p)
	}
	ordinary := backendDecode[posts.Post](t, backendCall(t, app, "GET", "/api/posts/"+p.ID, "", "", 200))
	if ordinary.Automod != nil {
		t.Fatal("private rule evidence exposed publicly")
	}
	path := "/api/admin/posts/" + p.ID + "/automod-review"
	request := c.AutomodReview{ExpectedRevision: p.Automod.Revision, Action: "dismiss", Reason: "Synthetic optional review"}
	backendJSONCall(t, app, "POST", path, f.outsideToken, request, 501)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	enabled, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, PostAutomodReview: true})
	if e != nil {
		t.Fatal(e)
	}
	backendJSONCall(t, enabled, "POST", path, f.token, request, 403)
	reviewed := backendDecode[c.Post](t, backendJSONCall(t, enabled, "POST", path, f.outsideToken, request, 200))
	if reviewed.Automod == nil || reviewed.Automod.Outcome != "dismissed" {
		t.Fatal(reviewed)
	}
	backendJSONCall(t, enabled, "POST", path, f.outsideToken, map[string]any{"expected_revision": reviewed.Automod.Revision, "action": "recheck", "reason": "Synthetic fresh reevaluation"}, 200)
}
