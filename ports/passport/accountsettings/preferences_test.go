// SPDX-License-Identifier: AGPL-3.0-only
package accountsettings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

type settingsAuth struct{}

func (settingsAuth) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer synthetic" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: "10000000-0000-4000-8000-000000000001"}}, nil
}

type settingsRepo struct {
	writes       int
	actor, topic string
}

func (r *settingsRepo) List(context.Context, string, string) ([]Preference, error) {
	return []Preference{}, nil
}
func (r *settingsRepo) Reset(context.Context, string, string, string) error { return nil }
func (r *settingsRepo) Set(_ context.Context, actor, app, topic string, v PreferenceInput) (Preference, error) {
	if !ValidPreference(v) {
		return Preference{}, ErrInvalid
	}
	r.writes++
	r.actor = actor
	r.topic = topic
	return Preference{Topic: topic, Preference: v.Preference}, nil
}
func TestOwnedPreferenceHTTPStrictLevelsAndIdentity(t *testing.T) {
	repo := &settingsRepo{}
	h := (&Service{Repository: repo, Authority: settingsAuth{}}).Handler()
	for _, raw := range []string{`{"preference":"reject"}`, `{"preference":"silent"}`, `{"preference":"normal","description":"Owned synthetic custom topic"}`} {
		r := httptest.NewRequest("PUT", preferencePath+"/synthetic.topic", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || repo.actor == "" || repo.topic != "synthetic.topic" {
			t.Fatal(w.Code)
		}
	}
	for _, raw := range []string{"", `null`, `{"preference":"silent","preference":"reject"}`, `{"preference":1}`, `{"preference":"invented"}`, `{"preference":"reject","account_id":"other"}`, "{\"preference\":\"normal\",\"description\":\"\xff\"}"} {
		before := repo.writes
		r := httptest.NewRequest("PUT", preferencePath+"/synthetic.topic", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || repo.writes != before {
			t.Fatal(raw, w.Code)
		}
	}
	if ValidTopic("*") || ValidTopic("synthetic/*") {
		t.Fatal("unverified wildcard policy")
	}
}
