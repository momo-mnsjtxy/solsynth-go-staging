// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExactPriceRejectsSilentTruncation(t *testing.T) {
	for _, v := range []string{"0", "-1", "1.2345", "NaN", "1e100"} {
		if _, e := ExactPrice(json.Number(v)); e == nil {
			t.Fatal("accepted", v)
		}
	}
	v, e := ExactPrice(json.Number("9007199254740993.123"))
	if e != nil || v.String() != "9007199254740993.123" {
		t.Fatal(v, e)
	}
}

func TestDefinitionFreeTrialPolicyIsExplicitAndBounded(t *testing.T) {
	v := DefinitionInput{Identifier: "trial", GroupIdentifier: "trial", DisplayName: "Trial", Currency: "points", BasePrice: json.Number("1"), CycleDurationDays: 30, PayeeWalletID: "a7000000-0000-0000-0000-000000000001", AllowedPaymentMethods: []string{"solian.wallet"}, FreeTrialDays: 14}
	d, e := ValidateDefinition(v)
	if e != nil || d.FreeTrialDays != 14 {
		t.Fatal(d, e)
	}
	v.FreeTrialDays = 367
	if _, e = ValidateDefinition(v); e == nil {
		t.Fatal("unbounded trial accepted")
	}
}
func TestBoundedDateAndExplicitUnlink(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	from, to := now.Add(-367*24*time.Hour), now
	if _, e := NormalizeQuery(Query{From: &from, To: &to}, now); e == nil {
		t.Fatal("unbounded range")
	}
	r := httptest.NewRequest("GET", "/api/merchants/p/orders?from=2026-01-01&to=2026-01-01", nil)
	q, e := query(r, false)
	if e != nil || q.To.Sub(*q.From) != 24*time.Hour {
		t.Fatal(q, e)
	}
	for _, body := range []string{`{}`, `{"other":null}`, `{"wallet_id":true}`} {
		var p WalletPatch
		if json.Unmarshal([]byte(body), &p) == nil {
			t.Fatal("implicit or invalid unlink", body)
		}
	}
	var p WalletPatch
	if e = json.Unmarshal([]byte(`{"wallet_id":null}`), &p); e != nil || p.WalletID != nil {
		t.Fatal(e)
	}
}
