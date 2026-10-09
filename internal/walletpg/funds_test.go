// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/wallet/accounts"
)

func TestFundsFailClosedAndBoundedInvalidations(t *testing.T) {
	r := New(nil)
	if _, e := r.ProcessFundsDue(context.Background(), 1); !errors.Is(e, accounts.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.ProcessFundsDue(context.Background(), 0); !errors.Is(e, accounts.ErrInvalid) {
		t.Fatal(e)
	}
	text := strings.Repeat("x", 4096)
	f := accounts.Fund{Message: &text, Recipients: make([]accounts.FundRecipient, 512)}
	raw, e := json.Marshal(fundEvent(f))
	if e != nil || len(raw) > 8192 || strings.Contains(string(raw), "recipients") {
		t.Fatal(len(raw), e)
	}
}
