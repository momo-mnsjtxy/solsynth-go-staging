// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"encoding/json"
	"solsynth.local/go-migration/ports/messager"
	"time"
)

// Subscription/device containers are pinned to the authored controllers.
// DeviceToken is a non-credential owned connection identifier. Other members'
// device lists are always empty in room-wide projections.
type SubscriptionDevice struct {
	DeviceToken string    `json:"device_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type RoomSubscription struct {
	RoomID    string               `json:"room_id"`
	MemberID  string               `json:"member_id"`
	AccountID string               `json:"account_id"`
	Member    Member               `json:"member"`
	Devices   []SubscriptionDevice `json:"devices"`
}
type AccountSubscription struct {
	RoomID   string               `json:"room_id"`
	MemberID string               `json:"member_id"`
	Room     Room                 `json:"room"`
	Devices  []SubscriptionDevice `json:"devices"`
}
type SubscriptionDeviceStatus struct {
	SubscriptionDevice
	IsWebSocketConnected bool `json:"is_web_socket_connected"`
}
type SubscriptionRoomStatus struct {
	RoomID                      string                     `json:"room_id"`
	MemberID                    string                     `json:"member_id"`
	Room                        Room                       `json:"room"`
	IsSubscribed                bool                       `json:"is_subscribed"`
	PushNotificationsSuppressed bool                       `json:"push_notifications_suppressed"`
	Devices                     []SubscriptionDeviceStatus `json:"devices"`
}
type ChatAccountStatus struct {
	AccountID                                    string                   `json:"account_id"`
	HasActiveSubscriptions                       bool                     `json:"has_active_subscriptions"`
	HasAnyWebSocketConnection                    bool                     `json:"has_any_web_socket_connection"`
	PushNotificationsMaySendForUnsubscribedRooms bool                     `json:"push_notifications_may_send_for_unsubscribed_rooms"`
	Subscriptions                                []SubscriptionRoomStatus `json:"subscriptions"`
}
type GlobalSyncRequest struct {
	LastSyncTimestamp     int64                    `json:"last_sync_timestamp"`
	LastSyncMessageID     *string                  `json:"last_sync_message_id"`
	MissingSequences      []int64                  `json:"missing_sequences"`
	MissingSequenceRanges []messager.SequenceRange `json:"missing_sequence_ranges"`
}
type GlobalSyncResponse struct {
	Messages         []messager.MessageProjection `json:"messages"`
	CurrentTimestamp time.Time                    `json:"current_timestamp"`
	CurrentMessageID *string                      `json:"current_message_id"`
	TotalCount       int                          `json:"total_count"`
}
type JoinedRoomSyncRequest struct {
	LastSyncTimestamp int64 `json:"last_sync_timestamp"`
}
type JoinedRoomChange struct {
	RoomID    string    `json:"room_id"`
	Type      string    `json:"type"`
	Room      *Room     `json:"room"`
	Member    *Member   `json:"member"`
	ChangedAt time.Time `json:"changed_at"`
}
type JoinedRoomSummary struct {
	RoomID string `json:"room_id"`
	ChatSummary
	ChangedAt time.Time `json:"changed_at"`
}
type JoinedRoomSyncResponse struct {
	Changes           []JoinedRoomChange  `json:"changes"`
	Summaries         []JoinedRoomSummary `json:"summaries"`
	Groups            []ChatGroup         `json:"groups"`
	CurrentTimestamp  time.Time           `json:"current_timestamp"`
	TotalCount        int                 `json:"total_count"`
	SummaryTotalCount int                 `json:"summary_total_count"`
}
type SubscriptionSyncRepository interface {
	RoomSubscriptions(context.Context, string, string) ([]RoomSubscription, error)
	AccountSubscriptions(context.Context, string) ([]AccountSubscription, error)
	ChatStatus(context.Context, string) (ChatAccountStatus, error)
	GlobalSync(context.Context, string, GlobalSyncRequest) (GlobalSyncResponse, error)
	JoinedRoomSync(context.Context, string, JoinedRoomSyncRequest) (JoinedRoomSyncResponse, error)
}

var ErrSyncBucketOverflow = &Failure{"CHAT_SYNC_BUCKET_OVERFLOW", "The timestamp bucket exceeds the supported sync bound; the cursor was not advanced.", 409}

func ValidateGlobalSync(q GlobalSyncRequest) error {
	if !ValidSyncTimestamp(q.LastSyncTimestamp) || q.LastSyncMessageID != nil && !ValidID(*q.LastSyncMessageID) {
		return ErrInvalid
	}
	// Sequence recovery is room scoped. Never pretend a global sequence has meaning.
	if len(q.MissingSequences) > 0 || len(q.MissingSequenceRanges) > 0 {
		return ErrUnsupported
	}
	return nil
}
func ValidSyncTimestamp(v int64) bool { return v >= 0 && v <= 253402300799999 }

// Required timestamps distinguish a first sync (zero) from a malformed payload.
func (q *GlobalSyncRequest) UnmarshalJSON(b []byte) error {
	type wire GlobalSyncRequest
	var v wire
	var keys map[string]json.RawMessage
	if json.Unmarshal(b, &keys) != nil || len(keys["last_sync_timestamp"]) == 0 || string(keys["last_sync_timestamp"]) == "null" {
		return ErrInvalid
	}
	if e := json.Unmarshal(b, &v); e != nil {
		return e
	}
	*q = GlobalSyncRequest(v)
	return nil
}
func (q *JoinedRoomSyncRequest) UnmarshalJSON(b []byte) error {
	type wire JoinedRoomSyncRequest
	var v wire
	var keys map[string]json.RawMessage
	if json.Unmarshal(b, &keys) != nil || len(keys["last_sync_timestamp"]) == 0 || string(keys["last_sync_timestamp"]) == "null" {
		return ErrInvalid
	}
	if e := json.Unmarshal(b, &v); e != nil {
		return e
	}
	*q = JoinedRoomSyncRequest(v)
	return nil
}
