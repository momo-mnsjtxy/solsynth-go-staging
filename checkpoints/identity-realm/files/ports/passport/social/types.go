// SPDX-License-Identifier: AGPL-3.0-only
// Independently owned social domain; licensed client evidence is documented in docs/social-continuation.md.
package social

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/presence"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"SOCIAL_INVALID", "Invalid social request.", 400}
	ErrForbidden   = &Failure{"SOCIAL_FORBIDDEN", "Permission denied.", 403}
	ErrNotFound    = &Failure{"SOCIAL_NOT_FOUND", "Resource not found.", 404}
	ErrConflict    = &Failure{"SOCIAL_CONFLICT", "Request conflicts with current state.", 409}
	ErrUnavailable = &Failure{"SOCIAL_UNAVAILABLE", "Social dependency unavailable.", 503}
	ErrUnsupported = &Failure{"SOCIAL_UNSUPPORTED", "This operation requires an unconfigured provider.", 501}
	ErrQuota       = &Failure{"REALM_QUOTA_EXCEEDED", "Realm quota is unavailable or exceeded.", 403}
)

// Values are evidenced by authored Solian relationship.dart and realm_detail.dart, not excluded shared definitions.
const (
	Blocked     = -100
	Muted       = -50
	Pending     = 0
	Friend      = 100
	CloseFriend = 200
)

type Role int

const (
	Normal    Role = 0
	Moderator Role = 50
	Owner     Role = 100
)

func (r Role) Valid() bool { return r >= Normal && r <= Owner }

type Relationship struct {
	ID        string            `json:"id"`
	AccountID string            `json:"account_id"`
	RelatedID string            `json:"related_id"`
	Status    int               `json:"status"`
	Alias     *string           `json:"alias"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	ExpiredAt *time.Time        `json:"expired_at"`
	Account   *accounts.Account `json:"account,omitempty"`
	Related   *accounts.Account `json:"related,omitempty"`
}
type RelationshipAction string

const (
	RequestFriend RelationshipAction = "request"
	AcceptFriend  RelationshipAction = "accept"
	DeclineFriend RelationshipAction = "decline"
	CancelFriend  RelationshipAction = "cancel"
	Block         RelationshipAction = "block"
	Mute          RelationshipAction = "mute"
	Unmute        RelationshipAction = "unmute"
	Forget        RelationshipAction = "forget"
	SetClose      RelationshipAction = "close"
	UnsetClose    RelationshipAction = "unclose"
	SetAlias      RelationshipAction = "alias"
	SetStatus     RelationshipAction = "status"
)

type RelationshipChange struct {
	ExpiresIn *string
	DegradeTo *int
	Action    RelationshipAction
	Status    int
	Alias     *string
}
type Realm struct {
	BoostPoints  int64                      `json:"boost_points"`
	Verification *accounts.VerificationMark `json:"verification,omitempty"`
	ID           string                     `json:"id"`
	Slug         string                     `json:"slug"`
	Name         string                     `json:"name"`
	Description  string                     `json:"description"`
	AccountID    string                     `json:"account_id"`
	IsPublic     bool                       `json:"is_public"`
	IsCommunity  bool                       `json:"is_community"`
	BoostLevel   int                        `json:"boost_level"`
	CreatedAt    time.Time                  `json:"created_at"`
	UpdatedAt    time.Time                  `json:"updated_at"`
}
type RealmPatch struct {
	Slug         *string `json:"slug"`
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	IsPublic     *bool   `json:"is_public"`
	IsCommunity  *bool   `json:"is_community"`
	PictureID    *string `json:"picture_id"`
	BackgroundID *string `json:"background_id"`
}
type Member struct {
	Status               *presence.Status   `json:"status,omitempty"`
	Nick                 *string            `json:"nick"`
	Bio                  *string            `json:"bio"`
	LabelID              *string            `json:"label_id,omitempty"`
	Label                *RealmLabel        `json:"label,omitempty"`
	Progression          *MemberProgression `json:"progression,omitempty"`
	EffectivePermissions *RealmPermissions  `json:"effective_permissions,omitempty"`
	ID                   string             `json:"id"`
	RealmID              string             `json:"realm_id"`
	AccountID            string             `json:"account_id"`
	Role                 Role               `json:"role"`
	JoinedAt             *time.Time         `json:"joined_at"`
	LeaveAt              *time.Time         `json:"leave_at"`
	CreatedAt            time.Time          `json:"created_at"`
	UpdatedAt            time.Time          `json:"updated_at"`
	Realm                *Realm             `json:"realm,omitempty"`
	Account              *accounts.Account  `json:"account,omitempty"`
}
type MemberAction string

const (
	Invite     MemberAction = "invite"
	Accept     MemberAction = "accept"
	Decline    MemberAction = "decline"
	Join       MemberAction = "join"
	Leave      MemberAction = "leave"
	Remove     MemberAction = "remove"
	ChangeRole MemberAction = "role"
)

type MemberChange struct {
	Action    MemberAction
	AccountID string
	Role      Role
}
type InviteRequest struct {
	RelatedUserID string `json:"related_user_id"`
	Role          Role   `json:"role"`
}
type Repository interface {
	Relationships(context.Context, string, bool, int, int) ([]Relationship, int64, error)
	Relationship(context.Context, string, string) (Relationship, error)
	ChangeRelationship(context.Context, string, string, RelationshipChange) (Relationship, error)
	CreateRealm(context.Context, string, RealmPatch) (Realm, error)
	GetRealm(context.Context, string, string) (Realm, error)
	ListRealms(context.Context, string, int, int) ([]Realm, error)
	UpdateRealm(context.Context, string, string, RealmPatch) (Realm, error)
	DeleteRealm(context.Context, string, string) error
	Members(context.Context, string, string, int, int) ([]Member, int64, error)
	Me(context.Context, string, string) (Member, error)
	Invites(context.Context, string, int, int) ([]Member, error)
	ChangeMember(context.Context, string, string, MemberChange) (Member, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}

// Consumers pass their own transaction to retain one atomic snapshot. These
// reads never start transactions or acquire additional account locks.
type RelationshipProvider interface {
	IsBlockedInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
	CanReadFriendsInTransaction(context.Context, *sql.Tx, string, string, bool) (bool, error)
}
type RealmProvider interface {
	RealmRoleInTransaction(context.Context, *sql.Tx, string, string) (Role, error)
}
