// SPDX-License-Identifier: AGPL-3.0-only
package presence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

type rejectAuthority struct{}

func (rejectAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}
func (rejectAuthority) AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}
func (rejectAuthority) AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}

type emptyRepository struct{ Repository }

func TestPresenceHTTPDeniesForgedAndEmptyCredentials(t *testing.T) {
	s := Service{Repository: emptyRepository{}, Authority: rejectAuthority{}, Bind: func(c context.Context, _ authority.Principal) context.Context { return c }}
	for _, tc := range []struct {
		path, header, value string
		status              int
	}{{"/api/accounts/other/statuses", "Authorization", "", 401}, {"/api/accounts/other/statuses", "X-Account-ID", "forged", 403}, {"/api/accounts/me/statuses?token=forged", "", "", 400}, {"/api/accounts/me/statuses", "", "", 401}} {
		r := httptest.NewRequest("GET", tc.path, nil)
		if tc.header != "" {
			r.Header[tc.header] = []string{tc.value}
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}
func TestPresenceBounds(t *testing.T) {
	v := StatusRequest{}
	if ValidateRequest(v) != nil {
		t.Fatal("defaults")
	}
	v.Type = 4
	if ValidateRequest(v) == nil {
		t.Fatal("unknown type")
	}
	v.Type = 0
	s := strings.Repeat("x", 1025)
	v.Label = &s
	if ValidateRequest(v) == nil {
		t.Fatal("unbounded label")
	}
	if ValidatePreferences(Preferences{OnlineVisibility: "friends", LastSeenVisibility: "private", StatusVisibility: "public"}) != nil {
		t.Fatal("known privacy")
	}
	if ValidatePreferences(Preferences{}) == nil {
		t.Fatal("blank privacy")
	}
}
