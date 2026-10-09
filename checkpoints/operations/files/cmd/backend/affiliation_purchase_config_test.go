// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
)

func TestAffiliationPurchaseConfiguration(t *testing.T) {
	binding := []string{
		"--enable-paid-affiliation-invites",
		"--affiliation-invite-currency=points",
		"--affiliation-invite-treasury-account=00000000-0000-4000-8000-000000000001",
		"--affiliation-invite-treasury-wallet=00000000-0000-4000-8000-000000000002",
	}
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"disabled defaults", nil, true},
		{"disabled binding", binding[1:], false},
		{"enabled", append(append([]string{}, binding...), "--affiliation-invite-price=12.500", "--affiliation-invite-max-usages=3", "--affiliation-invite-max-lifetime=48h"), true},
		{"missing currency", []string{"--enable-paid-affiliation-invites", "--affiliation-invite-treasury-account=00000000-0000-4000-8000-000000000001", "--affiliation-invite-treasury-wallet=00000000-0000-4000-8000-000000000002"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			s := affiliationPurchaseFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cfg := application.Config{}
			err := s.apply(&cfg)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if tc.name == "enabled" && (cfg.AffiliationPurchase == nil || cfg.AffiliationPurchase.Price.String() != "12.500" || cfg.AffiliationPurchase.MaxUsages != 3 || cfg.AffiliationPurchase.MaxLifetime != 48*time.Hour) {
				t.Fatal(cfg.AffiliationPurchase)
			}
		})
	}
}
