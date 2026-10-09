// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
)

func TestOperationsRetentionRequiresExplicitBoundedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"disabled defaults", nil, true},
		{"disabled override", []string{"--domain-metric-retention=48h"}, false},
		{"enabled", []string{"--enable-operations-retention", "--domain-metric-retention=48h", "--transfer-request-retention=72h", "--operations-retention-batch=17"}, true},
		{"too short", []string{"--enable-operations-retention", "--transfer-request-retention=59m"}, false},
		{"empty batch", []string{"--enable-operations-retention", "--operations-retention-batch=0"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			s := operationsRetentionFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			cfg := application.Config{}
			err := s.apply(&cfg)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if tc.name == "enabled" && (cfg.OperationsRetention == nil || cfg.OperationsRetention.DomainMetrics != 48*time.Hour || cfg.OperationsRetention.TransferRequests != 72*time.Hour || cfg.OperationsRetention.Batch != 17) {
				t.Fatal(cfg.OperationsRetention)
			}
		})
	}
}
