// SPDX-License-Identifier: AGPL-3.0-only
// Package notifications implements an independently owned durable inbox with
// route/field evidence from authored licensed Solian callers. It is not Ring.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"
)

var (
	ErrInvalid              = errors.New("invalid notification request")
	ErrUnavailable          = errors.New("notification dependency unavailable")
	ErrNotFound             = errors.New("notification not found")
	ErrConflict             = errors.New("notification idempotency conflict")
	ErrCursor               = errors.New("realtime cursor requires reset")
	ErrDeliveryUnconfigured = errors.New("external notification delivery is not configured")
)

// HeadCursor asks the durable store to establish a fresh connection at the
// current committed high-water mark. It is an in-process sentinel and is never
// accepted from an explicit HTTP cursor.
const HeadCursor int64 = -1

// CursorReset identifies the nearest cursor from which a client can recover.
// Older cursors recover from the retained floor; cursors beyond the stream
// recover from the current high-water mark.
type CursorReset struct{ Cursor int64 }

func (e *CursorReset) Error() string        { return ErrCursor.Error() }
func (e *CursorReset) Is(target error) bool { return target == ErrCursor }

func RecoverableCursor(err error) (int64, bool) {
	var reset *CursorReset
	if errors.As(err, &reset) {
		return reset.Cursor, true
	}
	return 0, false
}

const DefaultApp = "dev.solsynth.solian"
const MaxPayload = 65536

var appPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidApp(v string) bool { return appPattern.MatchString(v) }

// Notification fields are evidenced by the authored watch model and inbox.
// DismissedAt and AppID are owned additions; GET returns pre-view timestamps.
type Notification struct {
	ID          string          `json:"id"`
	AccountID   string          `json:"account_id"`
	AppID       string          `json:"app_id"`
	Topic       string          `json:"topic"`
	Title       string          `json:"title"`
	Subtitle    string          `json:"subtitle"`
	Content     string          `json:"content"`
	Meta        json.RawMessage `json:"meta"`
	Priority    int             `json:"priority"`
	ViewedAt    *time.Time      `json:"viewed_at"`
	DismissedAt *time.Time      `json:"dismissed_at"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	DeletedAt   *time.Time      `json:"deleted_at"`
}

// Command is trusted in-process input, never exposed as a public send route.
// Key is durable and must identify the same logical input on every retry.
type Command struct {
	Key       string
	AccountID string
	AppID     string
	Topic     string
	Title     string
	Subtitle  string
	Content   string
	Meta      json.RawMessage
	Priority  int
	External  bool
}
type Event struct {
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}
type Page struct {
	Events    []Event `json:"events"`
	Cursor    int64   `json:"cursor"`
	HighWater int64   `json:"high_water"`
}
type Identity interface {
	Account(*http.Request) (string, error)
}
type Repository interface {
	List(context.Context, string, string, int, int) ([]Notification, int64, error)
	Unread(context.Context, string, string) (int64, error)
	MarkRead(context.Context, string, string, string) error
	Dismiss(context.Context, string, string, string) error
}
type EventStore interface {
	Replay(context.Context, string, string, int64, int) (Page, error)
}
type Delivery struct {
	Key          string
	Notification Notification
}

// Sender must honor ctx and deduplicate Delivery.Key. At-least-once retry
// includes the crash window after acceptance and before durable acknowledgment.
type Sender interface {
	Send(context.Context, Delivery) error
}
type UnconfiguredSender struct{}

func (UnconfiguredSender) Send(context.Context, Delivery) error { return ErrDeliveryUnconfigured }
