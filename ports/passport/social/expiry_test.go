// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthoredExpiryDurationsAndNullClears(t *testing.T) {
	values := map[string]time.Duration{"30m": 30 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 168 * time.Hour, "30d": 720 * time.Hour}
	for value, want := range values {
		got, e := RelationshipDuration(value)
		if e != nil || got != want {
			t.Fatal(value, got, e)
		}
	}
	for _, raw := range []string{`{"expires_in":"1h","expires_in":"7d"}`, `{"ExpiresIn":"1h"}`, `{"degrade_to":-50.5}`, `{"expires_in":1}`, `[]`, `{"extra":null}`} {
		var v ExpiryRequest
		if json.Unmarshal([]byte(raw), &v) == nil {
			t.Fatal("ambiguous expiry accepted", raw)
		}
	}
	var v ExpiryRequest
	if e := json.Unmarshal([]byte(`{"expires_in":"30m","degrade_to":-50}`), &v); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal([]byte(`{"expires_in":null,"degrade_to":null}`), &v); e != nil || v.ExpiresIn != nil || v.DegradeTo != nil {
		t.Fatal("reused request retained prior timer", v, e)
	}
	for _, c := range []RelationshipChange{{Action: Mute, DegradeTo: intPtr(Muted)}, {Action: Block, DegradeTo: intPtr(Friend), ExpiresIn: stringPtr("1h")}, {Action: Block, DegradeTo: intPtr(Muted)}, {Action: AcceptFriend, ExpiresIn: stringPtr("1h")}, {Action: Block, ExpiresIn: stringPtr("0s")}} {
		if ValidateRelationshipChange(c) == nil {
			t.Fatal(c)
		}
	}
}
func intPtr(v int) *int          { return &v }
func stringPtr(v string) *string { return &v }
func TestExpiryRoutesPreserveActorAndSpecificAuthority(t *testing.T) {
	cases := []struct {
		path, body, deny string
		action           RelationshipAction
		status           int
	}{{"block", `{"expires_in":"1h","degrade_to":-50}`, "", Block, 200}, {"mute", `{"expires_in":"30m"}`, "", Mute, 200}, {"unmute", `{}`, "", Unmute, 200}, {"mute", `{}`, "relationships.mute.manage", Mute, 403}, {"unmute", `{}`, "relationships.friends.manage", Unmute, 403}, {"friends", `{"expires_in":"1h"}`, "", RequestFriend, 400}}
	for _, tc := range cases {
		spy := &repositorySpy{}
		auth := &fakeAuthority{deny: tc.deny}
		r := httptest.NewRequest("POST", "/stargate/relationships/"+testTarget+"/"+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		(&Service{Repository: spy, Authority: auth}).Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		if tc.status == 200 && (spy.actor != testActor || spy.target != testTarget || spy.change.Action != tc.action) {
			t.Fatal(spy)
		}
		if tc.status != 200 && spy.calls != 0 {
			t.Fatal("denied request mutated")
		}
	}
}
