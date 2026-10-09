// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/wallet/commerce"
)

func TestCustomOrderExactAmountAndBounds(t *testing.T) {
	for _, tc := range []struct {
		amount string
		ok     bool
	}{{"0.001", true}, {"1.0010", true}, {"1e-3", true}, {"0", false}, {"-1", false}, {"0.0009", false}, {"1.0001", false}, {"1e1000000000", false}, {"79228162514264337593543950335", true}, {"79228162514264337593543950336", false}} {
		q := CustomOrderRequest{Identifier: "external invoice / 1", Currency: "points", Amount: CustomOrderAmount(tc.amount)}
		_, hours, e := ValidateCustomOrder(q)
		if (e == nil) != tc.ok || e == nil && hours != 24 {
			t.Fatal(tc, hours, e)
		}
	}
	for _, hours := range []int{-1, 0, 1, 720, 721} {
		q := CustomOrderRequest{Identifier: "invoice", Currency: "points", Amount: "1", ExpirationHours: &hours}
		_, _, e := ValidateCustomOrder(q)
		if (e == nil) != (hours >= 1 && hours <= 720) {
			t.Fatal(hours, e)
		}
	}
	for _, key := range []string{"subscription_id", "gift_id", "app_subscription", "fulfillment", "required_scopes", "address_contact_id", "address_snapshot"} {
		q := CustomOrderRequest{Identifier: "invoice", Currency: "points", Amount: "1", Meta: map[string]json.RawMessage{key: json.RawMessage(`{}`)}}
		if _, _, e := ValidateCustomOrder(q); e == nil {
			t.Fatal("reserved metadata accepted", key)
		}
	}
}

type managementTestRepo struct {
	Repository
	calls int
}

func (r *managementTestRepo) DeveloperStatistics(context.Context, string) (DeveloperStats, error) {
	r.calls++
	return DeveloperStats{TotalCustomApps: 7}, nil
}

type managementTestAuthority struct{}

func (managementTestAuthority) Require(context.Context, ...string) (authority.Principal, error) {
	return authority.Principal{Account: authority.Account{ID: "11111111-1111-4111-8111-111111111111"}}, nil
}

type managementTestOrders struct {
	calls int
	scope Scope
	req   CustomOrderRequest
}

func (s *managementTestOrders) CreateCustomOrder(_ context.Context, scope Scope, req CustomOrderRequest, _ string) (commerce.Order, error) {
	s.calls++
	s.scope, s.req = scope, req
	return commerce.Order{ID: "custom"}, nil
}

func TestManagementHTTPContractAndCredentialSeparation(t *testing.T) {
	repo, store := &managementTestRepo{}, &managementTestOrders{}
	s := &Service{Repository: repo, Authority: managementTestAuthority{}, CustomOrders: store}
	path := "/api/private/apps/22222222-2222-4222-8222-222222222222/orders?dev=sample&proj=33333333-3333-4333-8333-333333333333"
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/developers/sample/stats", "", 200},
		{"POST", "/api/developers/sample/stats", `{}`, 405},
		{"GET", "/api/developers/sample/stats?dev=other", "", 400},
		{"POST", path, `{"identifier":"invoice","currency":"points","amount":1.001,"expiration_hours":24}`, 200},
		{"POST", path, `{"identifier":"invoice","currency":"points","amount":1.0011}`, 400},
		{"POST", path, `{"identifier":"invoice","currency":"points","amount":"1.001"}`, 400},
		{"POST", path, `{"identifier":"invoice","currency":"points","amount":1,"client_secret":"forbidden"}`, 400},
		{"POST", path, `{"identifier":"invoice","currency":"points","amount":1,"items":[]}`, 400},
		{"POST", path + "&dev=duplicate", `{}`, 400},
		{"POST", path + "&unknown=1", `{}`, 400},
		{"GET", path, "", 405},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if IsAppCredentialRoute(req) {
			t.Fatal("custom order became app-secret route")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	if repo.calls != 1 || store.calls != 1 || store.scope.Developer != "sample" || store.req.Amount != "1.001" {
		t.Fatal(repo.calls, store)
	}
}
