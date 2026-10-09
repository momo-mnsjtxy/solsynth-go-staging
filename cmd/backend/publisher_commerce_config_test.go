// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"solsynth.local/go-migration/internal/application"
	"testing"
)

func TestPublisherCommerceExplicitConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{{nil, true}, {[]string{"-sponsor-treasury-account", "11111111-1111-4111-8111-111111111111"}, false}, {[]string{"-enable-post-sponsorship"}, false}, {[]string{"-enable-publisher-ratings"}, false}, {[]string{"-publisher-ratings-earliest-day", "2026-01-01"}, false}, {[]string{"-enable-publisher-ratings", "-publisher-ratings-earliest-day", "2026-01-01"}, true}, {[]string{"-enable-publisher-ratings", "-publisher-ratings-earliest-day", "2026-01-01", "-publisher-commerce-interval", "0s"}, false}, {[]string{"-enable-post-sponsorship", "-sponsor-treasury-account", "11111111-1111-4111-8111-111111111111", "-sponsor-treasury-wallet", "22222222-2222-4222-8222-222222222222"}, true}, {[]string{"-enable-post-sponsorship", "-sponsor-treasury-account", "11111111-1111-4111-8111-111111111111", "-sponsor-treasury-wallet", "22222222-2222-4222-8222-222222222222", "-sponsor-max-amount", "4.999"}, false}} {
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		settings := publisherCommerceFlags(fs)
		if e := fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		var c application.Config
		e := settings.apply(&c)
		if (e == nil) != tc.ok {
			t.Fatal(tc.args, e)
		}
		if e == nil && c.PublisherRatings != nil && !c.PublisherRatings.ReactionOnly {
			t.Fatal("implicit legacy parity mode")
		}
	}
}
