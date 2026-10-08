// SPDX-License-Identifier: AGPL-3.0-only
// Independently owned text-room lifecycle informed by licensed controllers.
package rooms

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"CHAT_INVALID", "Invalid text-room request.", 400}
	ErrForbidden   = &Failure{"CHAT_FORBIDDEN", "Active membership or room ownership required.", 403}
	ErrNotFound    = &Failure{"CHAT_NOT_FOUND", "Room, member or message not found.", 404}
	ErrConflict    = &Failure{"CHAT_CONFLICT", "Request conflicts with existing state.", 409}
	ErrUnsupported = &Failure{"CHAT_UNSUPPORTED", "This capability is not implemented by the owned text-room service.", 501}
	ErrUnavailable = &Failure{"CHAT_UNAVAILABLE", "Chat dependency unavailable.", 503}
)

// Policy values are supplied explicitly and shared with messagerpg reads. They
// are independent mappings, not reconstructed excluded shared enum values.
type Policy struct{ NoneEncryptionMode, NotifyAll, NotifyMentions, NotifyNone int32 }

func (p Policy) Validate() error {
	if p.NotifyAll == p.NotifyMentions || p.NotifyAll == p.NotifyNone || p.NotifyMentions == p.NotifyNone {
		return ErrInvalid
	}
	return nil
}
func (p Policy) ValidNotify(n int32) bool {
	return n == p.NotifyAll || n == p.NotifyMentions || n == p.NotifyNone
}

// Room and Member are explicitly versioned projections, not full legacy DTOs.
type Room struct {
	Type           int           `json:"type"`
	RealmID        *string       `json:"realm_id,omitempty"`
	Realm          *social.Realm `json:"realm,omitempty"`
	DirectMembers  []Member      `json:"direct_members,omitempty"`
	Members        []Member      `json:"members,omitempty"`
	RelationGuard  bool          `json:"-"`
	ID             string        `json:"id"`
	AccountID      string        `json:"account_id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	IsPublic       bool          `json:"is_public"`
	IsCommunity    bool          `json:"is_community"`
	EncryptionMode int32         `json:"encryption_mode"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	DeletedAt      *time.Time    `json:"deleted_at"`
}
type Member struct {
	ConfirmedAt  *time.Time        `json:"confirmed_at"`
	TimeoutUntil *time.Time        `json:"timeout_until"`
	TimeoutCause *TimeoutCause     `json:"timeout_cause"`
	Account      *accounts.Account `json:"account,omitempty"`
	ID           string            `json:"id"`
	ChatRoomID   string            `json:"chat_room_id"`
	AccountID    string            `json:"account_id"`
	Notify       int32             `json:"notify"`
	JoinedAt     *time.Time        `json:"joined_at"`
	LeaveAt      *time.Time        `json:"leave_at"`
	LastReadAt   *time.Time        `json:"last_read_at"`
	Room         *Room             `json:"chat_room,omitempty"`
}
type RoomPatch struct {
	RealmID        *string `json:"realm_id"`
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	IsPublic       *bool   `json:"is_public"`
	IsCommunity    *bool   `json:"is_community"`
	EncryptionMode *int32  `json:"encryption_mode"`
}

type TimeoutCause struct {
	Reason   *string   `json:"reason"`
	SenderID string    `json:"sender_id"`
	Since    time.Time `json:"since"`
}
type TimeoutRequest struct {
	Reason       *string   `json:"reason"`
	TimeoutUntil time.Time `json:"timeout_until"`
}
type DirectRequest struct {
	RelatedUserID  string `json:"related_user_id"`
	EncryptionMode *int32 `json:"encryption_mode"`
}
type RelationRepository interface {
	CreateDirect(context.Context, string, DirectRequest) (Room, error)
	GetDirect(context.Context, string, string) (Room, error)
	ChangeDirect(context.Context, string, string, string) (Room, error)
	RealmRooms(context.Context, string, string, int, int) ([]Room, error)
	Timeout(context.Context, string, string, string, *TimeoutRequest) error
}

// Consumers use the same additional visibility gate for owned linked rooms.
// Expressions passed to SQL methods are fixed internal aliases/placeholders.
type AccessPolicy interface {
	LockForAction(context.Context, *sql.Tx, string, string) error
	CanReadInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
	ReadSQL(string, string) string
	SendSQL(string, string) string
	CallSQL(string, string) string
}
type SocialProvider interface {
	IsBlockedInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
	RealmRoleInTransaction(context.Context, *sql.Tx, string, string) (social.Role, error)
	RealmRoleForWriteInTransaction(context.Context, *sql.Tx, string, string) (social.Role, error)
	RealmInTransaction(context.Context, *sql.Tx, string) (social.Realm, error)
}
type TextRequest struct {
	Content          string  `json:"content"`
	ClientMessageID  *string `json:"client_message_id"`
	Nonce            *string `json:"nonce"`
	RepliedMessageID *string `json:"replied_message_id"`
}
type MemberAction string

const (
	Join    MemberAction = "join"
	Invite  MemberAction = "invite"
	Accept  MemberAction = "accept"
	Decline MemberAction = "decline"
	Leave   MemberAction = "leave"
	Remove  MemberAction = "remove"
)

type Repository interface {
	Create(context.Context, string, RoomPatch) (Room, error)
	Get(context.Context, string, string) (Room, error)
	List(context.Context, string, int, int) ([]Room, error)
	Update(context.Context, string, string, RoomPatch) (Room, error)
	Delete(context.Context, string, string) error
	Members(context.Context, string, string, int, int) ([]Member, error)
	Me(context.Context, string, string) (Member, error)
	Invites(context.Context, string, int, int) ([]Member, error)
	ChangeMember(context.Context, string, string, MemberAction, string) (Member, error)
	SetNotify(context.Context, string, string, int32) (Member, error)
	MarkRead(context.Context, string, string, string) (Member, error)
	Send(context.Context, string, string, TextRequest) (messager.MessageProjection, error)
	Edit(context.Context, string, string, string, TextRequest) (messager.MessageProjection, error)
	DeleteMessage(context.Context, string, string, string) error
}

// Authority consumes the owned verifier's unforgeable proof and rechecks live
// account/session/permission state. No role or account header is an identity.
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}
