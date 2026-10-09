// SPDX-License-Identifier: AGPL-3.0-only
package notifications

import (
	"context"
	"database/sql"
	"time"
)

// ChatEventRefV1 is a private durable reference, never a wire payload. The exact
// recipient membership incarnation prevents a leave/rejoin from reviving replay.
// Existing owned.chat.changed events remain separate and retain their contract.
type ChatEventRefV1 struct {
	Version        int       `json:"version"`
	Kind           string    `json:"kind,omitempty"`
	MessageID      string    `json:"message_id"`
	ReceiptID      string    `json:"receipt_id,omitempty"`
	ExcludedDevice string    `json:"excluded_device_id,omitempty"`
	RoomID         string    `json:"chat_room_id"`
	MemberID       string    `json:"recipient_member_id"`
	JoinedAt       time.Time `json:"recipient_joined_at"`
}

type ChatEventProjector interface {
	ProjectChatEvent(context.Context, *sql.Tx, string, ChatEventRefV1) (Event, bool, error)
}
