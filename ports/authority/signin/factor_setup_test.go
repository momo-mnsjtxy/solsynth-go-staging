// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
	"time"
)

func TestSetupContinuationUsesAuthorizationGenerationAndFixedDeadline(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Service{cfg: Config{Now: func() time.Time { return now }}}
	p := authority.Principal{Account: authority.Account{Version: 3}, Session: authority.Session{ID: "same-root", Type: 0, Epoch: 2, AuthorizationEpoch: 1}}
	v := FactorSetup{SessionID: p.Session.ID, AccountVersion: 3, AuthorizationEpoch: 1, AuthTime: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute), PolicyHash: "policy", State: "pending"}
	if !s.setupValid(v, p, "policy") {
		t.Fatal("ordinary refresh rejected bounded continuation")
	}
	for _, kind := range []string{"session", "version", "authorization", "parent", "client", "policy", "deadline", "future", "complete"} {
		t.Run(kind, func(t *testing.T) {
			q := p
			r := v
			policy := "policy"
			switch kind {
			case "session":
				q.Session.ID = "other"
			case "version":
				q.Account.Version++
			case "authorization":
				q.Session.AuthorizationEpoch++
			case "parent":
				q.Session.ParentID = "parent"
			case "client":
				q.Session.ClientID = "delegated"
			case "policy":
				policy = "changed"
			case "deadline":
				r.ExpiresAt = now
			case "future":
				r.AuthTime = now.Add(time.Nanosecond)
			case "complete":
				r.State = "complete"
			}
			if s.setupValid(r, q, policy) {
				t.Fatal("invalid setup accepted")
			}
		})
	}
}
func TestFactorEnableBodyPreservesMissingNullAndString(t *testing.T) {
	for _, v := range []struct {
		name, raw      string
		present, valid bool
	}{{"missing", "", false, true}, {"null", "null", true, true}, {"string", "\"123456\"", true, true}, {"trailing", "null null", true, false}, {"object", "{}", true, false}, {"number", "123456", true, false}, {"oversized", strings.Repeat(" ", 16385), false, false}} {
		t.Run(v.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/stargate/factors/example/enable", strings.NewReader(v.raw))
			if v.raw != "" {
				r.Header.Set("Content-Type", "application/json")
			}
			_, present, e := decodeEnableBody(httptest.NewRecorder(), r)
			if (e == nil) != v.valid || e == nil && present != v.present {
				t.Fatal(present, e)
			}
		})
	}
}
