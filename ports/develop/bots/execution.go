// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"time"

	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
)

const ExecutionPermission = "developers.bots.execution.manage"
const MessagePermission = "chat.messages.create"

// ExecutionAdapter is configured by the host. Names identify a fixed deployment
// destination; request data may never choose a network URL, secret, or principal.
// Deliver must honor ctx, keep its identity/transport immutable, deduplicate
// EventID, and return nil only after durable acceptance. A retry after provider acceptance and failed SQL commit is possible.
// Without provider deduplication, repeated acceptance/execution is possible.
// Acceptance is not evidence that a command has completed or posted a reply.
type ExecutionAdapter interface {
	Name() string
	BotID() string
	BindingID() string
	Deliver(context.Context, MessageDelivery) error
}
type MessageDelivery struct {
	EventID         string          `json:"event_id"`
	EventType       string          `json:"event_type"`
	BotID           string          `json:"bot_id"`
	BotAccountID    string          `json:"bot_account_id"`
	RoomID          string          `json:"room_id"`
	MessageID       string          `json:"message_id"`
	SenderAccountID string          `json:"sender_account_id"`
	Content         string          `json:"content"`
	MessageType     string          `json:"message_type"`
	EncryptionMeta  json.RawMessage `json:"encryption_meta"`
	Meta            json.RawMessage `json:"meta"`
	CreatedAt       time.Time       `json:"created_at"`
}
type ExecutionConfig struct {
	NoneEncryptionMode int
	EnableMLSOpaque    bool
	MLSEncryptionMode  int
	SendTimeout        time.Duration
	EventLifetime      time.Duration
	MaxAttempts        int
	MaxPendingPerRoom  int
}

func (c ExecutionConfig) Validate() error {
	if c.NoneEncryptionMode < 0 || c.EnableMLSOpaque && (c.MLSEncryptionMode < 0 || c.MLSEncryptionMode == c.NoneEncryptionMode) || c.SendTimeout < time.Millisecond || c.SendTimeout > 30*time.Second || c.EventLifetime < time.Second || c.EventLifetime > 24*time.Hour || c.MaxAttempts < 1 || c.MaxAttempts > 20 || c.MaxPendingPerRoom < 1 || c.MaxPendingPerRoom > 10000 {
		return ErrInvalid
	}
	return nil
}

var adapterName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func ValidAdapterName(s string) bool { return adapterName.MatchString(s) }

type EnrollmentRequest struct {
	Adapter   string    `json:"adapter"`
	ExpiresAt time.Time `json:"expires_at"`
}
type RenewalRequest struct {
	ExpiresAt time.Time `json:"expires_at"`
}
type RenewalPolicyRequest struct {
	// ExtendBySeconds is the bounded period each successful reconciliation adds
	// from its current database time. ConsentExpiresAt is an absolute hard stop;
	// it is never extended by the worker and cannot exceed the authorizing
	// session or seven days from the explicit request.
	ExtendBySeconds    int64     `json:"extend_by_seconds"`
	RenewBeforeSeconds int64     `json:"renew_before_seconds"`
	ConsentExpiresAt   time.Time `json:"consent_expires_at"`
}
type RenewalPolicy struct {
	BotID              string     `json:"bot_id"`
	RoomID             string     `json:"room_id"`
	ExtendBySeconds    int64      `json:"extend_by_seconds"`
	RenewBeforeSeconds int64      `json:"renew_before_seconds"`
	ConsentExpiresAt   time.Time  `json:"consent_expires_at"`
	Active             bool       `json:"active"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
	Reason             *string    `json:"reason,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
type Enrollment struct {
	BotID      string    `json:"bot_id"`
	RoomID     string    `json:"room_id"`
	Adapter    string    `json:"adapter"`
	Generation int64     `json:"generation"`
	Active     bool      `json:"active"`
	ExpiresAt  time.Time `json:"expires_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type ExecutionRepository interface {
	Enrollment(context.Context, apps.Scope, string, string, *EnrollmentRequest, string) (Enrollment, error)
	RoomCommands(context.Context, string, string) (map[string][]develop.BotCommand, error)
}
type RenewalRepository interface {
	RenewalPolicy(context.Context, apps.Scope, string, string, *RenewalPolicyRequest, string) (RenewalPolicy, error)
}

// MessageObserver runs inside the canonical chat write transaction and must not
// commit it. It records only IDs/provenance; authority is checked again at send.
type MessageObserver interface {
	QueueMessage(context.Context, *sql.Tx, string, string, string) error
}
