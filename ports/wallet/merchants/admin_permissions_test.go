// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

type walletAdminCapture struct {
	AdminRepository
	calls int
}

func (s *walletAdminCapture) AdminOrders(context.Context, string, AdminQuery) (AdminPage[Order], error) {
	s.calls++
	return AdminPage[Order]{Items: []Order{}}, nil
}
func (s *walletAdminCapture) AdminOrder(context.Context, string, string) (Order, error) {
	s.calls++
	return Order{}, nil
}
func (s *walletAdminCapture) AdminInboundOrders(context.Context, string, AdminQuery) (AdminPage[InboundOrder], error) {
	s.calls++
	return AdminPage[InboundOrder]{Items: []InboundOrder{}}, nil
}
func (s *walletAdminCapture) AdminInboundOrder(context.Context, string, string) (InboundOrder, error) {
	s.calls++
	return InboundOrder{}, nil
}
func (s *walletAdminCapture) WalletProductSummary(context.Context, string) (json.RawMessage, error) {
	s.calls++
	return json.RawMessage(`{"identifier":"synthetic"}`), nil
}
func (s *walletAdminCapture) ApplyWalletProduct(context.Context, string, string, WalletProductApply, string) (wallet.Transaction, error) {
	s.calls++
	return wallet.Transaction{}, nil
}

type walletAdminAuth struct {
	grant, asked string
}

func (*walletAdminAuth) Authenticate(*http.Request) (string, error) { return "synthetic", nil }
func (a *walletAdminAuth) Authorize(_ *http.Request, permission string) error {
	a.asked = permission
	if permission != a.grant {
		return ErrForbidden
	}
	return nil
}

func TestWalletAdminRoutesRequireSeparateOperatorPermissions(t *testing.T) {
	const id = "a7000000-0000-0000-0000-000000000088"
	for _, tc := range []struct {
		method, path, body, permission, customerPermission string
	}{
		{"GET", "/api/admin/payments/orders", "", PermissionAdminOrdersView, "orders.view"},
		{"GET", "/api/admin/payments/orders/" + id, "", PermissionAdminOrdersView, "orders.view"},
		{"GET", "/api/admin/payments/inbound-orders", "", PermissionAdminOrdersView, "orders.view"},
		{"GET", "/api/admin/payments/inbound-orders/" + id, "", PermissionAdminOrdersView, "orders.view"},
		{"GET", "/api/admin/wallet-products/golds-resupply-pack", "", PermissionAdminProductsView, "orders.view"},
		{"POST", "/api/admin/wallet-products/orders/" + id + "/apply", `{"treasury_wallet_id":"` + id + `"}`, PermissionAdminOrdersManage, "orders.pay"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			for _, grant := range []string{tc.customerPermission, tc.permission} {
				repo := &walletAdminCapture{}
				auth := &walletAdminAuth{grant: grant}
				h := NewHandler(&extendedRepository{AdminRepository: repo}, auth)
				r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want, calls := 403, 0
				if grant == tc.permission {
					want, calls = 200, 1
				}
				if w.Code != want || repo.calls != calls || auth.asked != tc.permission {
					t.Fatalf("grant %q: status=%d calls=%d permission=%q body=%s", grant, w.Code, repo.calls, auth.asked, w.Body.String())
				}
			}
		})
	}
}
