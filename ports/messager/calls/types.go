// SPDX-License-Identifier: AGPL-3.0-only
// Package calls implements signaling; media transport remains the provider's responsibility.
package calls

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid      = &Failure{"CHAT_CALL_INVALID", "Invalid call request.", 400}
	ErrForbidden    = &Failure{"CHAT_CALL_FORBIDDEN", "Active room membership and appropriate authority required.", 403}
	ErrNotFound     = &Failure{"CHAT_CALL_NOT_FOUND", "Call or participant not found.", 404}
	ErrConflict     = &Failure{"CHAT_CALL_CONFLICT", "Call transition or participant removal is pending.", 409}
	ErrUnsupported  = &Failure{"CHAT_CALL_UNSUPPORTED", "This call capability is not supported.", 501}
	ErrUnavailable  = &Failure{"CHAT_CALL_UNAVAILABLE", "Call dependency unavailable.", 503}
	ErrProvider     = &Failure{"CHAT_CALL_PROVIDER_UNAVAILABLE", "Provider operation failed; remote completion is not confirmed.", 503}
	ErrUnconfigured = &Failure{"CHAT_CALL_PROVIDER_UNCONFIGURED", "A call provider with strict token revocation must be configured.", 503}
)

type Call struct {
	ID               string        `json:"id"`
	RoomID           string        `json:"room_id"`
	SenderID         string        `json:"sender_id"`
	ProviderEndpoint string        `json:"-"`
	ProviderName     string        `json:"provider_name"`
	SessionID        string        `json:"session_id"`
	State            string        `json:"state"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	ExpiresAt        time.Time     `json:"expires_at"`
	EndedAt          *time.Time    `json:"ended_at"`
	Room             *rooms.Room   `json:"room,omitempty"`
	Sender           *rooms.Member `json:"sender,omitempty"`
}
type Participant struct {
	State     string        `json:"state,omitempty"`
	Identity  string        `json:"identity"`
	Name      string        `json:"name"`
	AccountID string        `json:"account_id"`
	JoinedAt  time.Time     `json:"joined_at"`
	TrackSID  string        `json:"track_sid,omitempty"`
	Profile   *rooms.Member `json:"profile,omitempty"`
}
type JoinResponse struct {
	Provider     string        `json:"provider"`
	Endpoint     string        `json:"endpoint"`
	Token        string        `json:"token"`
	CallID       string        `json:"call_id"`
	RoomName     string        `json:"room_name"`
	RoomTitle    string        `json:"room_title"`
	IsAdmin      bool          `json:"is_admin"`
	Participants []Participant `json:"participants"`
	ExpiresAt    time.Time     `json:"expires_at"`
}
type KickRequest struct {
	BanDurationMinutes int    `json:"ban_duration_minutes"`
	Reason             string `json:"reason"`
}
type Grant struct {
	Room, Identity      string
	IssuedAt, ExpiresAt time.Time
}

// Provider methods are idempotent or safely retryable. Remove must confirm both
// participant disconnection and revocation of all older tokens (including provider refreshes).
// Implementations must not claim that a self-hosted disconnect revokes tokens.
type Provider interface {
	Name() string
	Endpoint() string
	StrictRevocation() bool
	EnsureRoom(context.Context, string) error
	DeleteRoom(context.Context, string) error
	Participants(context.Context, string) ([]Participant, error)
	Remove(context.Context, string, string, time.Time) error
	Mute(context.Context, string, string, string, bool) error
	Token(context.Context, Grant) (string, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}
type Repository interface {
	Start(context.Context, string, string) (Call, error)
	Get(context.Context, string, string) (Call, error)
	Join(context.Context, string, string, string) (JoinResponse, error)
	Participants(context.Context, string, string) ([]Participant, error)
	Leave(context.Context, string, string, string) error
	Kick(context.Context, string, string, string, KickRequest) error
	Mute(context.Context, string, string, string, bool) error
	Invite(context.Context, string, string, string) error
	End(context.Context, string, string) error
}
