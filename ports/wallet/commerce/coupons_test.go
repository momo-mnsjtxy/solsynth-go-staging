// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"encoding/json"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
	"time"
)

func couponAmount(t *testing.T, s string) wallet.Amount {
	t.Helper()
	a, e := wallet.ParseAmount(s)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func couponFixture() Coupon {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return Coupon{ID: "10000000-0000-4000-8000-000000000001", Identifier: "SAVE001", AppID: "10000000-0000-4000-8000-000000000002", PublisherID: "10000000-0000-4000-8000-000000000003", PayeeWalletID: "10000000-0000-4000-8000-000000000004", Enabled: true, Policy: CouponPolicy{SellerFixedMills, RetainCouponUsage}, CreatedAt: now, Terms: CouponTerms{Type: "fixed_mills", Scope: "order", Currency: "synthetic", Discount: "0.123", Products: []string{"widget"}, Audience: "all", Accounts: []string{}, StartsAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: 10, MaxUsesPerAccount: 1}}
}
func TestCouponExactMillsConservationAndTotalRounding(t *testing.T) {
	c := couponFixture()
	actor := "10000000-0000-4000-8000-000000000005"
	for _, v := range []struct {
		price string
		qty   int
		want  string
	}{{"2.1239", 2, "4.124"}, {"9007199254740993.123", 1, "9007199254740993.000"}, {"0.0009", 1000, "0.777"}} {
		gross, e := Total([]OrderItem{{UnitPrice: json.Number(v.price), Quantity: v.qty}})
		if e != nil {
			t.Fatal(e)
		}
		q, e := QuoteCoupon(c, actor, c.AppID, c.PayeeWalletID, "order", "synthetic", []string{"widget"}, gross, c.Terms.StartsAt)
		if e != nil || q.Net.String() != v.want {
			t.Fatal(q, e)
		}
		sum, e := q.Net.Add(q.Discount)
		if e != nil || sum.Cmp(gross) != 0 {
			t.Fatal("value created or destroyed", sum, e)
		}
	}
	for _, n := range []string{"0", "-1", "0.1234", "0.0001", "1/2", "NaN", "1e10000", "79228162514264337593543950336"} {
		if _, e := ExactDiscount(json.Number(n)); e == nil {
			t.Fatal("accepted imprecise or invalid discount", n)
		}
	}
	for _, gross := range []string{"0.123", "0.122"} {
		if _, e := QuoteCoupon(c, actor, c.AppID, c.PayeeWalletID, "order", "synthetic", []string{"widget"}, couponAmount(t, gross), c.Terms.StartsAt); e == nil {
			t.Fatal("nonpositive net")
		}
	}
}
func TestCouponExplicitPolicyAndEligibilityFailClosed(t *testing.T) {
	original := couponFixture()
	actor := "10000000-0000-4000-8000-000000000005"
	for name, change := range map[string]func(*Coupon){"missing policy": func(c *Coupon) { c.Policy = CouponPolicy{} }, "unknown cancellation": func(c *Coupon) { c.Policy.Cancellation = "restore" }, "numeric type": func(c *Coupon) { c.Terms.Type = "0" }, "unknown scope": func(c *Coupon) { c.Terms.Scope = "platform" }, "omitted audience": func(c *Coupon) { c.Terms.Audience = "" }, "foreign audience": func(c *Coupon) { c.Terms.Audience = "accounts"; c.Terms.Accounts = []string{c.AppID} }, "revoked": func(c *Coupon) { c.Enabled = false }, "not started": func(c *Coupon) { c.Terms.StartsAt = c.Terms.StartsAt.Add(time.Minute) }, "expired": func(c *Coupon) { c.Terms.ExpiresAt = c.Terms.StartsAt }, "wrong currency": func(c *Coupon) { c.Terms.Currency = "other" }, "wrong product": func(c *Coupon) { c.Terms.Products = []string{"other"} }, "omitted cap": func(c *Coupon) { c.Terms.MaxUses = 0 }} {
		t.Run(name, func(t *testing.T) {
			c := original
			change(&c)
			if _, e := QuoteCoupon(c, actor, c.AppID, c.PayeeWalletID, "order", "synthetic", []string{"widget"}, couponAmount(t, "1"), original.Terms.StartsAt); e == nil {
				t.Fatal("unsupported term admitted")
			}
		})
	}
	if (CouponPolicy{Type: SellerFixedMills}).Valid() || (CouponPolicy{Cancellation: RetainCouponUsage}).Valid() {
		t.Fatal("implicit policy")
	}
	for _, code := range []string{"", "x", strings.Repeat("a", 65), original.ID, "spaces here"} {
		if ValidCouponCode(code) {
			t.Fatal(code)
		}
	}
}
func TestCouponSnapshotFingerprintAndRestart(t *testing.T) {
	c := couponFixture()
	actor := "10000000-0000-4000-8000-000000000005"
	q, e := QuoteCoupon(c, actor, c.AppID, c.PayeeWalletID, "order", "synthetic", []string{"widget"}, couponAmount(t, "2"), c.Terms.StartsAt)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(q)
	if e != nil {
		t.Fatal(e)
	}
	var restored CouponQuote
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	if restored.Gross.String() != "2.000" || restored.Net.String() != "1.877" || restored.TermsHash != q.TermsHash {
		t.Fatal(restored)
	}
	c.Terms.Discount = "0.124"
	changed, _ := CouponFingerprint(c)
	if changed == q.TermsHash {
		t.Fatal("coupon changed without quote hash change")
	}
}
