// SPDX-License-Identifier: AGPL-3.0-only
package notifications

import (
	"context"
	"database/sql"
)

// Audience events stay local to the existing first-party account stream. The
// source contains durable IDs only; current authorization supplies its payload.
type AudienceEventPending struct{ ID, Recipient, Owner string }
type AudienceEventProjector interface {
	PendingAudienceEvents(context.Context, int) ([]AudienceEventPending, error)
	AudienceEventOwner(context.Context, *sql.Tx, string, string) (string, error)
	LockAudienceEvent(context.Context, *sql.Tx, string, string) (bool, error)
	ProjectAudienceEvent(context.Context, *sql.Tx, string, string) (Event, bool, error)
	CompleteAudienceEvent(context.Context, *sql.Tx, string, string) error
}

// RevalidateEvent is the last local observation before a transport write. It
// commits SQL locks before returning; bytes already written cannot be recalled.
type EventRevalidator interface {
	RevalidateEvent(context.Context, string, string, Event) (Event, bool, error)
}
