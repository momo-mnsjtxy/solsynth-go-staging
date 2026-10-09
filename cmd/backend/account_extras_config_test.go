// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"io"
	"solsynth.local/go-migration/internal/application"
	"testing"
)

func TestAccountExtrasExplicitSettlementConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args           []string
		valid, enabled bool
	}{
		{nil, true, false},
		{[]string{"--name-card-currency=synthetic"}, false, false},
		{[]string{"--enable-name-cards"}, false, false},
		{[]string{"--enable-name-cards", "--name-card-currency=synthetic", "--name-card-payee-wallet=10000000-0000-4000-8000-000000000001"}, true, true},
		{[]string{"--enable-name-cards", "--name-card-currency=synthetic", "--name-card-payee-wallet=10000000-0000-4000-8000-000000000001", "--name-card-price=0"}, false, false},
		{[]string{"--rewind-reporting-year=1800"}, false, false},
		{[]string{"--rewind-reporting-year=2026"}, true, false},
	} {
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		opts := accountExtraFlags(fs)
		if e := fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		var c application.Config
		e := opts.apply(&c)
		if (e == nil) != tc.valid {
			t.Fatal(tc.args, e)
		}
		if e == nil && (c.NameCards != nil) != tc.enabled {
			t.Fatal("unexpected paid enablement")
		}
	}
}
