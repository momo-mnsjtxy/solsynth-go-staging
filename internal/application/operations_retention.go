// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"errors"
	"time"
)

type OperationsRetentionConfig struct {
	DomainMetrics, TransferRequests time.Duration
	Batch                           int
}

func (c *OperationsRetentionConfig) valid() bool {
	return c != nil && c.DomainMetrics >= time.Hour && c.DomainMetrics <= 10*365*24*time.Hour && c.TransferRequests >= time.Hour && c.TransferRequests <= 10*365*24*time.Hour && c.Batch >= 1 && c.Batch <= 10000
}

func (b *Backend) processOperationsRetention(ctx context.Context) (int, error) {
	if b == nil || b.operationsRetention == nil {
		return 0, nil
	}
	count := int64(0)
	var result error
	if b.domainCollections.domains != nil {
		n, e := b.domainCollections.domains.PurgeMetricsDue(ctx, b.operationsRetention.DomainMetrics, b.operationsRetention.Batch)
		count += n
		result = errors.Join(result, e)
	}
	if b.wallet != nil {
		n, e := b.wallet.PurgeTransferRequestsDue(ctx, b.operationsRetention.TransferRequests, b.operationsRetention.Batch)
		count += n
		result = errors.Join(result, e)
	}
	return int(count), result
}
