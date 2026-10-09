// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import "net/http"

// These explicit extension routes do not claim an unavailable legacy coupon API.
func (s *Service) couponIssuerHTTP(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if len(parts) == 0 || parts[0] != "coupons" {
		return false
	}
	store, ok := s.Store.(CouponStore)
	if !ok {
		problem(w, ErrUnavailable, "COUPON_UNAVAILABLE")
		return true
	}
	var result any
	var e error
	switch {
	case len(parts) == 1 && r.Method == "POST":
		var q CreateCoupon
		e = body(r, &q)
		if e == nil {
			result, e = store.CreateCoupon(r.Context(), q, r.Header.Get("Idempotency-Key"))
		}
	case len(parts) == 3 && parts[2] == "revoke" && r.Method == "POST":
		var q RevokeCoupon
		e = body(r, &q)
		if e == nil {
			result, e = store.RevokeCoupon(r.Context(), parts[1], q)
		}
	default:
		problem(w, ErrNotFound, "COUPON_NOT_FOUND")
		return true
	}
	if e != nil {
		problem(w, e, "COUPON_FAILED")
	} else {
		reply(w, 200, result)
	}
	return true
}
