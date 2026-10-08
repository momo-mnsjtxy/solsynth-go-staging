// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"net/http/httptest"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
)

func TestOrderUpdateCannotChangePaymentEconomics(t *testing.T) {
	for _, field := range []string{`"amount":99`, `"currency":"replacement"`, `"payee_wallet_id":"12345678-1234-4234-9234-123456789012"`, `"payer_wallet_id":"12345678-1234-4234-9234-123456789012"`} {
		r := httptest.NewRequest("PATCH", "/orders/synthetic", strings.NewReader(`{"status":3,`+field+`}`))
		var v UpdateStatus
		if body(r, &v) == nil {
			t.Fatal("economic update accepted", field)
		}
	}
	payer := "12345678-1234-4234-9234-123456789012"
	other := "12345678-1234-4234-9234-123456789013"
	base := wallet.OrderPaymentBinding("synthetic-order", &payer)
	if base == wallet.OrderPaymentBinding("other-order", &payer) || base == wallet.OrderPaymentBinding("synthetic-order", &other) || base == wallet.OrderPaymentBinding("synthetic-order", nil) {
		t.Fatal("order/payer binding collapsed")
	}
}
