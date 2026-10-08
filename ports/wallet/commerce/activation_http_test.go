// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"context"
	"net/http"
	"net/http/httptest"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
)

type activationHTTPStore struct {
	Store
	switched             int
	actor, group, target string
}

func (s *activationHTTPStore) PendingActivations(context.Context, string, PageQuery) (PendingActivations, error) {
	return PendingActivations{Subscriptions: []Subscription{}}, nil
}
func (s *activationHTTPStore) SwitchSubscriptionActivation(_ context.Context, actor, group, target string) (GroupState, error) {
	s.switched++
	s.actor, s.group, s.target = actor, group, target
	return GroupState{GroupIdentifier: group}, nil
}

type activationHTTPIdentity struct {
	denied     bool
	permission *string
}

func (i activationHTTPIdentity) Authenticate(*http.Request) (wallet.Principal, error) {
	return wallet.Principal{AccountID: "00000000-0000-4000-8000-000000000001", SessionID: "synthetic"}, nil
}
func (i activationHTTPIdentity) Require(_ context.Context, _ wallet.Principal, p string) error {
	*i.permission = p
	if i.denied {
		return ErrForbidden
	}
	return nil
}
func TestActivationHTTPAuthoredRoutePermissionAndBody(t *testing.T) {
	store := &activationHTTPStore{}
	permission := ""
	identity := activationHTTPIdentity{permission: &permission}
	s := &Service{Store: store, Identity: identity}
	path := "/api/subscriptions/groups/synthetic-group/activate"
	id := "00000000-0000-4000-8000-000000000002"
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := call("POST", path, `{"subscription_id":"`+id+`"}`); w.Code != 200 || store.switched != 1 || permission != "subscriptions.groups.manage" || store.target != id {
		t.Fatal(w.Code, w.Body.String(), store, permission)
	}
	for _, body := range []string{`{}`, `{"subscription_id":"bad"}`, `{"subscription_id":"` + id + `","duration_days":99}`} {
		if w := call("POST", path, body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	identity.denied = true
	s.Identity = identity
	if w := call("POST", path, `{"subscription_id":"`+id+`"}`); w.Code != 403 || store.switched != 1 {
		t.Fatal(w.Code, store.switched)
	}
	if w := call("GET", "/api/subscriptions/pending-activations", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"next_activation_at":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
	s.Identity = nil
	if w := call("POST", path, `{"subscription_id":"`+id+`"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
