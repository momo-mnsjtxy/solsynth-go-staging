// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

type couponSettings struct{ policy, cancellation *string }

func couponFlags(f *flag.FlagSet) couponSettings {
	return couponSettings{f.String("coupon-policy", "", "explicit owned policy: seller_fixed_mills_v1; absent disables coupons"), f.String("coupon-cancellation-policy", "", "explicit coupon cancellation policy: retain_usage")}
}
func (s couponSettings) config() (c.CouponPolicy, error) {
	p := c.CouponPolicy{Type: *s.policy, Cancellation: *s.cancellation}
	if !p.Empty() && !p.Valid() {
		return c.CouponPolicy{}, errors.New("coupon policy and cancellation policy must both be explicitly supported")
	}
	return p, nil
}
