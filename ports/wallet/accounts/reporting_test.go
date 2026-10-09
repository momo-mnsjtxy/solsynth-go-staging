// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

type reportsFixture struct {
	Store
	calls    int
	stats    StatsQuery
	overview OverviewQuery
	orders   OrderHistoryQuery
}

func (s *reportsFixture) WalletStats(_ context.Context, _ string, q StatsQuery) (WalletStats, error) {
	s.calls++
	s.stats = q
	return WalletStats{}, nil
}
func (s *reportsFixture) WalletOverview(_ context.Context, _ string, q OverviewQuery) (WalletOverview, error) {
	s.calls++
	s.overview = q
	return WalletOverview{}, nil
}
func (s *reportsFixture) WalletOrders(_ context.Context, _ string, q OrderHistoryQuery) (OrderHistoryPage, error) {
	s.calls++
	s.orders = q
	return OrderHistoryPage{Total: 7}, nil
}
func TestReportingHTTPValidationAndNormalization(t *testing.T) {
	s := &reportsFixture{}
	h := NewHandler(&Service{Store: s}, authFixture{principal: Principal{accountA, "synthetic-session"}})
	for _, path := range []string{"/stats?period=-1", "/stats?period=3661", "/stats?period=1&period=2", "/stats?wallets=bad", "/stats?currencies=%00", "/overview?startDate=2026-01-02", "/overview?startDate=2026-01-02T00:00:00Z&endDate=2026-01-01T00:00:00Z", "/orders?offset=-1", "/orders?take=101", "/orders?type=2", "/orders?status=9", "/orders?wallet=" + walletA + "&wallet=" + walletB, "/orders?direction=sideways", "/orders?offset=no", "/orders?status=paid&status=finished"} {
		out := httptest.NewRecorder()
		before := s.calls
		h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets"+path, nil))
		if out.Code != 400 || s.calls != before {
			t.Fatalf("%s %d calls=%d", path, out.Code, s.calls)
		}
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets/stats?wallets="+walletB+"&wallets="+walletA+"&wallets="+walletA+"&currencies=%20PoInTs%20&currencies=points", nil))
	if out.Code != 200 || len(s.stats.Wallets) != 2 || s.stats.Wallets[0] != walletA || len(s.stats.Currencies) != 1 || s.stats.Currencies[0] != "points" || s.stats.Period != 30 {
		t.Fatal(out.Code, s.stats)
	}
	out = httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets/overview?startDate=2026-01-02T01:00:00%2B01:00", nil))
	if out.Code != 200 || s.overview.StartDate.Format(time.RFC3339) != "2026-01-02T00:00:00Z" || s.overview.EndDate != nil {
		t.Fatal(out.Code, s.overview)
	}
	out = httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets/orders?type=payment&status=finished&direction=outcome&take=0", nil))
	if out.Code != 200 || out.Header().Get("X-Total") != "7" || *s.orders.Type != 1 || *s.orders.Status != 3 || s.orders.Direction != "out" || s.orders.Take != 0 {
		t.Fatal(out.Code, s.orders)
	}
}
