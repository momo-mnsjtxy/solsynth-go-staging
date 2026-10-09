// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/internal/messagerpg"
	a "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
)

func TestOwnedWalletExchangeRoutePolicyAndSamePool(t *testing.T) {
	config := a.ExchangeConfig{Version: "synthetic-v1", TreasuryAccountID: "11000000-0000-0000-0000-000000000002", TreasuryWalletID: "21000000-0000-0000-0000-000000000002", Rates: []a.ExchangeRate{{SourceCurrency: "points", SourceAmount: "3", TargetCurrency: "golds", TargetAmount: "1"}}}
	public, e := publicCatalogs(Config{WalletExchange: &config})
	if e != nil {
		t.Fatal(e)
	}
	routed := false
	h := router{modules: modules{public: public, wallet: http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		routed = r.URL.Path == "/api/wallets/exchange"
		out.WriteHeader(202)
	})}}
	for _, path := range []string{"/api/wallets/exchange", "/wallet/wallets/exchange"} {
		routed = false
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("POST", path, nil))
		if out.Code != 202 || !routed {
			t.Fatal(path, out.Code, routed)
		}
	}
	// Even unconfigured mutation requests belong to the authenticated wallet
	// handler, which returns its explicit disabled policy, not the GET catalog.
	defaultPublic, err := publicCatalogs(Config{})
	if err != nil {
		t.Fatal(err)
	}
	h.public = defaultPublic
	routed = false
	disabled := httptest.NewRecorder()
	h.ServeHTTP(disabled, httptest.NewRequest("POST", "/api/wallets/exchange", nil))
	if disabled.Code != 202 || !routed {
		t.Fatal("default mutation bypassed wallet", disabled.Code)
	}
	h.public = public
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/wallet/wallets/exchange", nil))
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"source_amount":3`) || !strings.Contains(out.Body.String(), `"target_amount":1`) || strings.Contains(out.Body.String(), config.TreasuryAccountID) {
		t.Fatal(out.Code, out.Body.String())
	}
	visibility := int32(0)
	_, e = NewPostgres(new(sql.DB), new(sql.DB), Config{PublicVisibility: &visibility, MessagerPolicy: &messagerpg.IndependentPolicy{}, WalletExchange: &config, AllowNonAtomicAuthorityWrites: true})
	if e == nil || e.Error() != "wallet exchange requires canonical same-pool authority" {
		t.Fatal(e)
	}
}
