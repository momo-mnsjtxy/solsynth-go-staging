// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"context"
	"time"
)

type LocationEvent struct {
	Sequence   uint64    `json:"sequence"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Replay stores transition metadata only; a stream always projects fresh current
// state under today's audience instead of replaying historical coordinates.
type ReplayRepository interface {
	LocationEvents(context.Context, string, string, bool, *Point, uint64, int) ([]LocationEvent, uint64, error)
}
