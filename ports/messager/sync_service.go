// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed read projection; see sync-PROVENANCE.md.
package messager

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const SyncPageLimit = 1000
const MaxRecoverySelectors = 4096 // Candidate resource bound, not an ASP.NET contract.
const SyncProjectionVersion = "synthetic-room-sync-projection-v1"

var (
	ErrSyncPolicyUnverified = errors.New("messager: production read policy and full hydration unverified")
	ErrSyncInvalidRequest   = errors.New("messager: invalid read request")
	ErrSyncUnauthorized     = errors.New("messager: authenticated identity required")
	ErrSyncForbidden        = errors.New("messager: active room membership required")
	ErrSyncRoomNotFound     = errors.New("messager: room not found")
	ErrSyncMessageNotFound  = errors.New("messager: message not found")
	ErrSyncUnavailable      = errors.New("messager: read dependency unavailable")
)

// MessageProjection is deliberately NOT SnChatMessage. It exposes evidenced
// persisted fields and SQL reaction/thread hydration, without inventing a
// sender/account DTO, nested related messages, or a cryptographic implementation.
// JSONB is retained as RawMessage, preserving numbers beyond float64 precision.
type MessageProjection struct {
	ID                 string          `json:"id"`
	ChatRoomID         string          `json:"chat_room_id"`
	SenderID           string          `json:"sender_id"`
	Type               string          `json:"type"`
	RoomSequence       int64           `json:"room_sequence"`
	Content            *string         `json:"content"`
	ClientMessageID    *string         `json:"client_message_id"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	DeletedAt          *time.Time      `json:"deleted_at"`
	EditedAt           *time.Time      `json:"edited_at"`
	Meta               json.RawMessage `json:"meta"`
	MembersMentioned   json.RawMessage `json:"members_mentioned"`
	Attachments        json.RawMessage `json:"attachments"`
	EncryptionMeta     json.RawMessage `json:"encryption_meta"`
	ReactionsCount     json.RawMessage `json:"reactions_count"`
	RepliedMessageID   *string         `json:"replied_message_id"`
	ForwardedMessageID *string         `json:"forwarded_message_id"`
	ThreadID           *string         `json:"thread_id"`
	ReactionsMade      map[string]bool `json:"reactions_made"`
	ThreadRepliesCount int32           `json:"thread_replies_count"`
	IsThreadRoot       bool            `json:"is_thread_root"`
}

type SyncRequest struct {
	LastSyncTimestamp     int64           `json:"last_sync_timestamp"`
	LastSyncMessageID     *string         `json:"last_sync_message_id,omitempty"` // Room sync ignores this source field.
	MissingSequences      []int64         `json:"missing_sequences,omitempty"`
	MissingSequenceRanges []SequenceRange `json:"missing_sequence_ranges,omitempty"`
}

type SyncQuery struct {
	RoomID, AccountID string
	Request           SyncRequest
}
type MessageListQuery struct {
	RoomID, AccountID string
	Take, Offset      int32
}
type MessageQuery struct{ RoomID, AccountID, MessageID string }
type SyncResponse struct {
	Messages         []MessageProjection `json:"messages"`
	CurrentTimestamp time.Time           `json:"current_timestamp"`
	TotalCount       int32               `json:"total_count"`
}
type MessagePage struct {
	Messages   []MessageProjection
	TotalCount int32
}

type SyncReader interface {
	Sync(context.Context, SyncQuery) (SyncResponse, error)
	ListMessages(context.Context, MessageListQuery) (MessagePage, error)
	GetMessage(context.Context, MessageQuery) (MessageProjection, error)
}

type SyncService struct {
	reader    SyncReader
	synthetic bool
}

func NewSyncService(reader SyncReader) *SyncService { return &SyncService{reader: reader} }
func NewSyntheticProjectionSyncService(reader SyncReader) *SyncService {
	return &SyncService{reader: reader, synthetic: true}
}

// ValidateSyncID accepts only the canonical hyphenated GUID shape in this
// candidate. ASP.NET's broader GUID parsing is not claimed.
func ValidateSyncID(id string) error {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return ErrSyncInvalidRequest
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(id, "-", "")); err != nil {
		return ErrSyncInvalidRequest
	}
	return nil
}
func ValidateSyncQuery(q SyncQuery) error {
	if q.AccountID == "" {
		return ErrSyncUnauthorized
	}
	if ValidateSyncID(q.RoomID) != nil || ValidateSyncID(q.AccountID) != nil {
		return ErrSyncInvalidRequest
	}
	if len(q.Request.MissingSequences) > MaxRecoverySelectors || len(q.Request.MissingSequenceRanges) > MaxRecoverySelectors {
		return ErrSyncInvalidRequest
	}
	if !NewRecoveryPlan(q.Request.MissingSequences, q.Request.MissingSequenceRanges).UsesSequences() {
		// Explicit candidate timestamp range (Go JSON/SQL portability), not NodaTime parity.
		if q.Request.LastSyncTimestamp < -62135596800000 || q.Request.LastSyncTimestamp > 253402300799999 {
			return ErrSyncInvalidRequest
		}
	}
	return nil
}
func ValidateMessageListQuery(q MessageListQuery) error {
	if ValidateSyncID(q.RoomID) != nil || q.AccountID != "" && ValidateSyncID(q.AccountID) != nil || q.Take < 0 || q.Take > SyncPageLimit || q.Offset < 0 {
		return ErrSyncInvalidRequest
	}
	return nil
}
func ValidateMessageQuery(q MessageQuery) error {
	if ValidateMessageListQuery(MessageListQuery{RoomID: q.RoomID, AccountID: q.AccountID}) != nil || ValidateSyncID(q.MessageID) != nil {
		return ErrSyncInvalidRequest
	}
	return nil
}
func (s *SyncService) available() error {
	if s == nil || !s.synthetic {
		return ErrSyncPolicyUnverified
	}
	if s.reader == nil {
		return ErrSyncUnavailable
	}
	return nil
}
func (s *SyncService) Sync(ctx context.Context, q SyncQuery) (SyncResponse, error) {
	if err := s.available(); err != nil {
		return SyncResponse{}, err
	}
	if err := ValidateSyncQuery(q); err != nil {
		return SyncResponse{}, err
	}
	return s.reader.Sync(ctx, q)
}
func (s *SyncService) ListMessages(ctx context.Context, q MessageListQuery) (MessagePage, error) {
	if err := s.available(); err != nil {
		return MessagePage{}, err
	}
	if err := ValidateMessageListQuery(q); err != nil {
		return MessagePage{}, err
	}
	return s.reader.ListMessages(ctx, q)
}
func (s *SyncService) GetMessage(ctx context.Context, q MessageQuery) (MessageProjection, error) {
	if err := s.available(); err != nil {
		return MessageProjection{}, err
	}
	if err := ValidateMessageQuery(q); err != nil {
		return MessageProjection{}, err
	}
	return s.reader.GetMessage(ctx, q)
}

// SyncUnreadReader exposes the existing CountUnreadMessageForUser query family.
// Full chat summaries still require unavailable sender/account hydration.
type SyncUnreadReader interface {
	Unread(context.Context, string) (map[string]UnreadSummary, error)
}

func (s *SyncService) UnreadTotal(ctx context.Context, account string) (int32, error) {
	if err := s.available(); err != nil {
		return 0, err
	}
	if account == "" {
		return 0, ErrSyncUnauthorized
	}
	if ValidateSyncID(account) != nil {
		return 0, ErrSyncInvalidRequest
	}
	reader, ok := s.reader.(SyncUnreadReader)
	if !ok {
		return 0, ErrSyncUnavailable
	}
	counts, err := reader.Unread(ctx, account)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, summary := range counts {
		if summary.UnreadCount < 0 {
			return 0, ErrSyncUnavailable
		}
		total += int64(summary.UnreadCount)
		if total > 2147483647 {
			return 0, ErrUnreadCountOverflow
		}
	}
	return int32(total), nil
}
