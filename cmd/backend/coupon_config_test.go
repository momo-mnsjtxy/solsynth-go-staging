// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
)

func TestCouponConfigRequiresExplicitFinancialAndCancellationPolicy(t *testing.T) {
	for _, tc := range []struct {
		args           []string
		valid, enabled bool
	}{{nil, true, false}, {[]string{"-coupon-policy", c.SellerFixedMills}, false, false}, {[]string{"-coupon-cancellation-policy", c.RetainCouponUsage}, false, false}, {[]string{"-coupon-policy", c.SellerFixedMills, "-coupon-cancellation-policy", c.RetainCouponUsage}, true, true}, {[]string{"-coupon-policy", "legacy", "-coupon-cancellation-policy", c.RetainCouponUsage}, false, false}} {
		f := flag.NewFlagSet("test", flag.ContinueOnError)
		s := couponFlags(f)
		if e := f.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		p, e := s.config()
		if (e == nil) != tc.valid || p.Valid() != tc.enabled {
			t.Fatal(tc.args, p, e)
		}
	}
}
