// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"net/http/httptest"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/translationpg"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/sphere/autocomplete"
	"solsynth.local/go-migration/ports/sphere/translation"
	"sync/atomic"
	"testing"
)

func TestPGBackendTranslationComposition(t *testing.T) {
	f := newTranslationFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	config := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy}
	disabled, e := application.NewPostgres(f.db, nil, config)
	if e != nil {
		t.Fatal(e)
	}
	backendJSONCall(t, disabled, "POST", "/sphere/translate?to=fr&from=en", f.token, "Synthetic original", 503)
	gate, e := translationpg.NewSystemSubscriptions([]string{f.plan})
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	config.Translation = &translationpg.Config{Entitlement: gate, Provider: syntheticTranslation(func(context.Context, translation.Request) (string, error) {
		calls.Add(1)
		return "Synthetic translated", nil
	})}
	app, e := application.NewPostgres(f.db, nil, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/sphere/translate?to=fr&from=en", "/api/translate?to=fr&from=en"} {
		got := backendDecode[string](t, backendJSONCall(t, app, "POST", path, f.token, "Synthetic original", 200))
		if got != "Synthetic translated" {
			t.Fatal("translation projection")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("canonical cache key")
	}
	backendJSONCall(t, app, "POST", "/api/translate?to=fr", "", "Synthetic original", 401)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET status=2 WHERE id=$1::uuid`, f.subscription)
	backendJSONCall(t, app, "POST", "/sphere/translate?to=fr&from=en", f.token, "Synthetic original", 403)
	if calls.Load() != 1 {
		t.Fatal("cancelled entitlement reached provider")
	}
}
func TestPGBackendAutocompleteComposition(t *testing.T) {
	f := newBackendFixture(t)
	var name string
	if e := f.db.QueryRow(`SELECT name FROM owned_authority_accounts WHERE id=$1::uuid`, f.actor).Scan(&name); e != nil {
		t.Fatal(e)
	}
	body := struct {
		Content string `json:"content"`
	}{"@" + name}
	suggestions := backendDecode[[]autocomplete.Suggestion](t, backendJSONCall(t, f.app, "POST", "/sphere/autocomplete", f.token, body, 200))
	if len(suggestions) != 1 || suggestions[0].Type != "user" {
		t.Fatal("canonical account suggestions", suggestions)
	}
	backendJSONCall(t, f.app, "POST", "/api/autocomplete", "", body, 401)
	req := fixtureJSONRequest(t, "POST", "/api/autocomplete", f.token, body)
	req.Header.Set("X-Account-ID", f.actor)
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("forged identity", w.Code)
	}
	title := "Suggestion room"
	room := backendDecode[rooms.Room](t, backendJSONCall(t, f.app, "POST", "/messager/chat", f.token, rooms.RoomPatch{Name: &title}, 200))
	backendJSONCall(t, f.app, "POST", "/messager/chat/"+room.ID+"/autocomplete", f.token, body, 200)
	backendJSONCall(t, f.app, "POST", "/api/chat/"+room.ID+"/autocomplete", f.otherToken, body, 403)
}
