// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/ports/sphere/postawards"
	"testing"
)

func TestPostAwardExplicitEnablementAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{{nil, true}, {[]string{"--post-award-payout-policy=documented_80_v1"}, false}, {[]string{"--enable-post-awards"}, false}, {[]string{"--enable-post-awards", "--post-award-treasury-account=11111111-1111-4111-8111-111111111111", "--post-award-treasury-wallet=22222222-2222-4222-8222-222222222222"}, true}, {[]string{"--publisher-ratings-paid-awards"}, false}} {
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		s := postAwardFlags(fs)
		if e := fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		var c application.Config
		e := s.apply(&c)
		if (e == nil) != tc.ok {
			t.Fatal(tc, e)
		}
		if c.PostAwards != nil && c.PostAwards.Policy != postawards.MerchantFull {
			t.Fatal("implicit fee", c.PostAwards)
		}
	}
}
