// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/messager"
)

// These projections retain the authored controller's response containers and
// evidenced persisted fields. Message and room objects remain owned projections.
type ChatSummary struct {
	UnreadCount int32                       `json:"unread_count"`
	HasUnread   bool                        `json:"has_unread"`
	LastMessage *messager.MessageProjection `json:"last_message"`
}
type SearchRequest struct {
	Query         string
	RoomIDs       []string
	Sender        string
	After, Before *time.Time
	Take, Offset  int
}
type SearchRoom struct {
	Room     Room                         `json:"room"`
	Messages []messager.MessageProjection `json:"messages"`
}
type SearchPage struct {
	Rooms []SearchRoom
	Total int64
}
type PinRequest struct {
	MessageID string     `json:"message_id"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type Pin struct {
	ID               string     `json:"id"`
	MessageID        string     `json:"message_id"`
	ChatRoomID       string     `json:"chat_room_id"`
	PinnedByMemberID string     `json:"pinned_by_member_id"`
	PinnedBy         *Member    `json:"pinned_by"`
	ExpiresAt        *time.Time `json:"expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DeletedAt        *time.Time `json:"deleted_at"`
}
type ChatGroup struct {
	ID        string     `json:"id"`
	AccountID string     `json:"account_id"`
	Name      string     `json:"name"`
	Color     *string    `json:"color"`
	Icon      *string    `json:"icon"`
	Order     int32      `json:"order"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}
type GroupPatch struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
	Icon  *string `json:"icon"`
	Order *int32  `json:"order"`
}
type GroupMember struct {
	Member
	ChatGroupID *string    `json:"chat_group_id"`
	ChatGroup   *ChatGroup `json:"chat_group"`
}
type OrganizationRepository interface {
	Summary(context.Context, string) (map[string]ChatSummary, error)
	ReadAll(context.Context, string) error
	Search(context.Context, string, SearchRequest) (SearchPage, error)
	Pins(context.Context, string, string, bool) ([]Pin, error)
	Pin(context.Context, string, string, PinRequest) (Pin, error)
	Unpin(context.Context, string, string, string) error
	Groups(context.Context, string) ([]ChatGroup, error)
	CreateGroup(context.Context, string, GroupPatch) (ChatGroup, error)
	UpdateGroup(context.Context, string, string, GroupPatch) (ChatGroup, error)
	DeleteGroup(context.Context, string, string) error
	MoveToGroup(context.Context, string, string, *string) error
	GroupMember(context.Context, string, string) (GroupMember, error)
}

// The caller supplies a bound canonical transaction, never arbitrary SQL.
type OrganizationMessageReader interface {
	OrganizationMessages(context.Context, *sql.DB, *sql.Tx, string, []string) ([]messager.MessageProjection, error)
}
type SearchEntitlement interface {
	RequireChatSearch(context.Context, *sql.DB, *sql.Tx, string, time.Time) error
}

var (
	ErrSearchQuery       = &Failure{"CHAT_SEARCH_QUERY_REQUIRED", "Search query cannot be empty.", 400}
	ErrSearchDates       = &Failure{"CHAT_SEARCH_DATE_RANGE_INVALID", "The after timestamp must be earlier than the before timestamp.", 400}
	ErrSearchEntitlement = &Failure{"CHAT_SEARCH_PERK_REQUIRED", "Perk level 1 is required to search cloud messages.", 403}
	ErrPinFailed         = &Failure{"CHAT_PIN_FAILED", "Message unavailable or already pinned in this room.", 400}
	ErrUnpinFailed       = &Failure{"CHAT_UNPIN_FAILED", "Pin not found in this room.", 400}
	ErrGroupMove         = &Failure{"CHAT_GROUP_MOVE_FAILED", "Failed to move the room to the specified group.", 400}
)

func ValidateSearch(q SearchRequest) (SearchRequest, error) {
	if utf16Length(q.Query) > 256 || utf16Length(q.Sender) > 256 {
		return q, ErrInvalid
	}
	q.Query = strings.TrimSpace(q.Query)
	q.Sender = strings.TrimSpace(q.Sender)
	if q.Query == "" {
		return q, ErrSearchQuery
	}
	if !utf8.ValidString(q.Query) || !utf8.ValidString(q.Sender) || strings.ContainsRune(q.Query, 0) || strings.ContainsRune(q.Sender, 0) || q.Take < 1 || q.Take > 100 || q.Offset < 0 || q.Offset > 100000 || len(q.RoomIDs) > 100 {
		return q, ErrInvalid
	}
	if q.After != nil && q.Before != nil && !q.After.Before(*q.Before) {
		return q, ErrSearchDates
	}
	for _, t := range []*time.Time{q.After, q.Before} {
		if t != nil && (t.Year() < 1 || t.Year() > 9999) {
			return q, ErrInvalid
		}
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range q.RoomIDs {
		if !ValidID(id) {
			return q, ErrInvalid
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	q.RoomIDs = ids
	return q, nil
}
func ValidateGroup(q GroupPatch, create bool) error {
	if create && q.Name == nil {
		return ErrInvalid
	}
	for _, v := range []struct {
		p *string
		n int
	}{{q.Name, 256}, {q.Color, 32}, {q.Icon, 64}} {
		if v.p != nil && (!utf8.ValidString(*v.p) || utf16Length(*v.p) > v.n || strings.ContainsRune(*v.p, 0)) {
			return ErrInvalid
		}
	}
	if q.Name != nil && strings.TrimSpace(*q.Name) == "" {
		return ErrInvalid
	}
	return nil
}

func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}
