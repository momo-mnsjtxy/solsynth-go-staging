// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"time"

	"solsynth.local/go-migration/internal/application"
)

type operationsRetentionSettings struct {
	enabled                  *bool
	domainMetrics, transfers *time.Duration
	batch                    *int
}

func operationsRetentionFlags(fs *flag.FlagSet) operationsRetentionSettings {
	return operationsRetentionSettings{
		enabled:       fs.Bool("enable-operations-retention", false, "enable bounded durable metrics and transfer-request retention"),
		domainMetrics: fs.Duration("domain-metric-retention", 90*24*time.Hour, "retention for aggregate domain trust metrics"),
		transfers:     fs.Duration("transfer-request-retention", 365*24*time.Hour, "retention for expired or terminal transfer requests"),
		batch:         fs.Int("operations-retention-batch", 1000, "maximum rows purged per lifecycle tick and family"),
	}
}

func (s operationsRetentionSettings) apply(c *application.Config) error {
	defaults := *s.domainMetrics == 90*24*time.Hour && *s.transfers == 365*24*time.Hour && *s.batch == 1000
	if !*s.enabled {
		if !defaults {
			return errors.New("operations retention policy requires explicit enablement")
		}
		return nil
	}
	if *s.domainMetrics < time.Hour || *s.domainMetrics > 10*365*24*time.Hour || *s.transfers < time.Hour || *s.transfers > 10*365*24*time.Hour || *s.batch < 1 || *s.batch > 10000 {
		return errors.New("invalid operations retention policy")
	}
	c.OperationsRetention = &application.OperationsRetentionConfig{DomainMetrics: *s.domainMetrics, TransferRequests: *s.transfers, Batch: *s.batch}
	return nil
}
