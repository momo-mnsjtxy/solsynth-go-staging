// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"solsynth.local/go-migration/ports/develop/bots"
)

// BotExecutionConfig binds operator-provided adapters to exact canonical bot IDs.
// Endpoints and signing keys belong to adapter construction, never HTTP input.
type BotExecutionConfig struct {
	Runtime  bots.ExecutionConfig
	Adapters []bots.ExecutionAdapter
}

// ProcessBotMessagesDue reconciles persisted messages using current authority.
func (b *Backend) ProcessBotMessagesDue(ctx context.Context, limit int) (int, error) {
	if b == nil || b.bots == nil || !b.bots.ExecutionEnabled() {
		return 0, bots.ErrUnsupported
	}
	return b.bots.ProcessMessagesDue(ctx, limit)
}
