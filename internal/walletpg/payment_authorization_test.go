// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
)

func TestAtomicPaymentRequiresCanonicalProviderAndExactBinding(t *testing.T) {
	p := NewPINVerifier(nil, nil)
	if _, e := p.Atomic().PreparePayment(context.Background(), accounts.Principal{}, "", accounts.PaymentTransfer, strings.Repeat("a", 64)); !errors.Is(e, accounts.ErrUnavailable) {
		t.Fatal(e)
	}
	for _, v := range [][2]string{{"arbitrary", strings.Repeat("a", 64)}, {accounts.PaymentOrder, "short"}, {accounts.PaymentTransfer, strings.Repeat("A", 64)}} {
		if paymentOperation(v[0], v[1]) {
			t.Fatal(v)
		}
	}
	if e := p.CheckPayment(context.Background(), nil, nil, "", accounts.PaymentOrder, strings.Repeat("a", 64)); !errors.Is(e, accounts.ErrUnavailable) {
		t.Fatal(e)
	}
}
