// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"time"
)

type Operation struct {
	Kind          string    `json:"kind"`
	ID            string    `json:"id"`
	StreamID      string    `json:"stream_id"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
}

type OperationRepository interface {
	InspectOperations(context.Context, string, string, int, int) ([]Operation, int, error)
	RedriveOperation(context.Context, string, string, string) (Operation, error)
}
