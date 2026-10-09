// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/passport/accounts"
	"time"
)

const MaxDataPacketBytes = 15 * 1024

var ErrChatMuted = &Failure{"LIVE_STREAM_CHAT_MUTED", "You are temporarily muted in this stream.", 403}
var ErrChatPacket = &Failure{"LIVE_STREAM_CHAT_PACKET_TOO_LARGE", "The serialized message exceeds the realtime packet limit.", 413}

type ChatMessage struct {
	ID            string            `json:"id"`
	LiveStreamID  string            `json:"live_stream_id"`
	SenderID      string            `json:"sender_id"`
	Sender        *accounts.Account `json:"sender"`
	SenderName    string            `json:"sender_name"`
	Content       string            `json:"content"`
	TimeoutUntil  *time.Time        `json:"timeout_until"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	DeletedAt     *time.Time        `json:"deleted_at"`
	DeliveryState string            `json:"delivery_state,omitempty"`
}
type ChatTimeout struct {
	DurationMinutes *int `json:"duration_minutes"`
}
type ChatRepository interface {
	ListChat(context.Context, string, string, int, int) ([]ChatMessage, error)
	SendChat(context.Context, string, string, string, string) (ChatMessage, error)
	DeleteChat(context.Context, string, string, string) error
	TimeoutChat(context.Context, string, string, string, string, ChatTimeout) error
}
type ChatAccountProvider interface {
	AccountInTransaction(context.Context, *sql.Tx, string, string) (accounts.Account, error)
}

// SendStreamData acknowledges provider acceptance, never client receipt. The
// nonempty destinations are previously recorded, freshly eligible admissions.
// An empty list must fail instead of broadcasting to an unfiltered room.
type DataProvider interface {
	SendStreamData(context.Context, string, string, []string, []byte) error
}
