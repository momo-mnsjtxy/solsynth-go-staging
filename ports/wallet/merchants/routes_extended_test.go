// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

type extendedRepository struct {
	Repository
	AdminRepository
}
type adminCapture struct {
	AdminRepository
	adjustment BalanceAdjustment
	key        string
	calls      int
}

func (s *adminCapture) AdjustBalance(_ context.Context, _ string, v BalanceAdjustment, key string) (wallet.Transaction, error) {
	s.adjustment = v
	s.key = key
	s.calls++
	return wallet.Transaction{ID: "a7000000-0000-0000-0000-000000000099"}, nil
}
func (s *adminCapture) Billing(_ context.Context, _ string, _ AdminQuery) (AdminPage[InboundOrder], error) {
	s.calls++
	return AdminPage[InboundOrder]{Items: []InboundOrder{{ID: "a7000000-0000-0000-0000-000000000088"}}, Total: 1}, nil
}

type authCapture struct {
	permissions []string
	deny        bool
}

func (*authCapture) Authenticate(*http.Request) (string, error) {
	return "a7000000-0000-0000-0000-000000000001", nil
}
func (a *authCapture) Authorize(_ *http.Request, p string) error {
	a.permissions = append(a.permissions, p)
	if a.deny {
		return ErrForbidden
	}
	return nil
}

func TestExtendedAdminBalanceAndBuyerBillingRoutes(t *testing.T) {
	admin := &adminCapture{}
	auth := &authCapture{}
	h := NewHandler(&extendedRepository{AdminRepository: admin}, auth)
	req := httptest.NewRequest("POST", "/api/admin/payments/balance", strings.NewReader(`{"wallet_id":"a7000000-0000-0000-0000-000000000002","treasury_wallet_id":"a7000000-0000-0000-0000-000000000003","currency":"points","amount":2.5}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "adjust-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || admin.calls != 1 || admin.key != "adjust-1" || admin.adjustment.Amount.String() != "2.500" || len(auth.permissions) != 1 || auth.permissions[0] != "wallets.balance.modify" {
		t.Fatal(w.Code, admin.calls, admin.key, admin.adjustment, auth.permissions, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/subscriptions/billing?take=10", nil))
	if w.Code != 200 || w.Header().Get("X-Total") != "1" || admin.calls != 2 {
		t.Fatal(w.Code, w.Header(), admin.calls, w.Body.String())
	}
}

func TestExtendedAdminStrictBodyStopsRepository(t *testing.T) {
	admin := &adminCapture{}
	h := NewHandler(&extendedRepository{AdminRepository: admin}, &authCapture{})
	req := httptest.NewRequest("POST", "/api/admin/payments/balance", strings.NewReader(`{"amount":1,"force_operation":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || admin.calls != 0 {
		t.Fatal(w.Code, admin.calls, w.Body.String())
	}
}

func TestExtendedAdminDeniedBeforeRepository(t *testing.T) {
	admin := &adminCapture{}
	h := NewHandler(&extendedRepository{AdminRepository: admin}, &authCapture{deny: true})
	req := httptest.NewRequest("POST", "/api/admin/payments/balance", strings.NewReader(`{"wallet_id":"a7000000-0000-0000-0000-000000000002","treasury_wallet_id":"a7000000-0000-0000-0000-000000000003","currency":"points","amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "denied")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 || admin.calls != 0 {
		t.Fatal(w.Code, admin.calls, w.Body.String())
	}
}
