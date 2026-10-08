// SPDX-License-Identifier: AGPL-3.0-only
// Independently owned text-room lifecycle informed by licensed controllers.
package rooms

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
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
	ID             string     `json:"id"`
	AccountID      string     `json:"account_id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	IsPublic       bool       `json:"is_public"`
	IsCommunity    bool       `json:"is_community"`
	EncryptionMode int32      `json:"encryption_mode"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at"`
}
type Member struct {
	ID         string     `json:"id"`
	ChatRoomID string     `json:"chat_room_id"`
	AccountID  string     `json:"account_id"`
	Notify     int32      `json:"notify"`
	JoinedAt   *time.Time `json:"joined_at"`
	LeaveAt    *time.Time `json:"leave_at"`
	LastReadAt *time.Time `json:"last_read_at"`
	Room       *Room      `json:"chat_room,omitempty"`
}
type RoomPatch struct {
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	IsPublic       *bool   `json:"is_public"`
	IsCommunity    *bool   `json:"is_community"`
	EncryptionMode *int32  `json:"encryption_mode"`
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
