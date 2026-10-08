// SPDX-License-Identifier: AGPL-3.0-only
// Owned account and publisher domain. See docs/account-publishers-continuation.md.
package accounts

import (
	"context"
	"errors"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrNotFound    = &Failure{"NOT_FOUND", "The requested resource does not exist.", 404}
	ErrForbidden   = &Failure{"FORBIDDEN", "Permission denied.", 403}
	ErrInvalid     = &Failure{"VALIDATION_ERROR", "Invalid request.", 400}
	ErrConflict    = &Failure{"CONFLICT", "The requested resource conflicts with existing state.", 409}
	ErrUnavailable = &Failure{"ACCOUNTS_UNAVAILABLE", "The account dependency is unavailable.", 503}
	ErrUnsupported = &Failure{"DEPENDENCY_UNSUPPORTED", "This operation requires an unconfigured provider.", 501}
	ErrNameChange  = &Failure{"NAME_CHANGE_CARD_REQUIRED", "Renaming requires the paid name-change-card workflow.", 409}
	ErrQuota       = &Failure{"PUBLISHER_QUOTA_EXCEEDED", "Publisher quota exceeded.", 403}
	ErrPolicy      = &Failure{"PUBLISHER_POLICY_UNVERIFIED", "No current publisher policy is configured.", 503}
	ErrAmbiguous   = &Failure{"PUBLISHER_SELECTION_REQUIRED", "Select a publisher or configure a default.", 409}
)

func NormalizeError(e error) error {
	if e == nil {
		return nil
	}
	var f *Failure
	if errors.As(e, &f) {
		return e
	}
	var a *authority.Failure
	if errors.As(e, &a) {
		return e
	}
	return ErrUnavailable
}

// Deliberate public projection: no contacts, credentials, grants, punishment,
// superuser flag or private authority metadata is ever serialized here.
type Account struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Nick      string     `json:"nick"`
	Language  string     `json:"language"`
	Region    string     `json:"region"`
	Profile   *Profile   `json:"profile,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}
type Profile struct {
	ID       string `json:"id"`
	Bio      string `json:"bio"`
	Pronouns string `json:"pronouns"`
	Location string `json:"location"`
	TimeZone string `json:"time_zone"`
	Links    []Link `json:"links"`
}
type Link struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type ProfilePatch struct {
	Name         *string `json:"name"`
	Nick         *string `json:"nick"`
	Language     *string `json:"language"`
	Region       *string `json:"region"`
	Bio          *string `json:"bio"`
	Pronouns     *string `json:"pronouns"`
	Location     *string `json:"location"`
	TimeZone     *string `json:"time_zone"`
	Links        *[]Link `json:"links"`
	PictureID    *string `json:"picture_id"`
	BackgroundID *string `json:"background_id"`
}

// Role values are owned API encoding, not copied from the excluded Shared enum.
type Role int

const (
	Viewer  Role = 0
	Editor  Role = 50
	Manager Role = 100
	Owner   Role = 200
)

func (r Role) Valid() bool { return r == Viewer || r == Editor || r == Manager || r == Owner }

type Publisher struct {
	ID        string    `json:"id"`
	Type      int       `json:"type"`
	Name      string    `json:"name"`
	Nick      string    `json:"nick"`
	Bio       string    `json:"bio"`
	AccountID *string   `json:"account_id,omitempty"`
	RealmID   *string   `json:"realm_id,omitempty"`
	Account   *Account  `json:"account,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Member struct {
	ID          string     `json:"id"`
	PublisherID string     `json:"publisher_id"`
	AccountID   string     `json:"account_id"`
	Role        Role       `json:"role"`
	JoinedAt    *time.Time `json:"joined_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Account     *Account   `json:"account,omitempty"`
	Publisher   *Publisher `json:"publisher,omitempty"`
}
type PublisherPatch struct {
	Name                  *string   `json:"name"`
	Nick                  *string   `json:"nick"`
	Bio                   *string   `json:"bio"`
	PictureID             *string   `json:"picture_id"`
	BackgroundID          *string   `json:"background_id"`
	PayoutWalletID        *string   `json:"payout_wallet_id"`
	DefaultPostTags       *[]string `json:"default_post_tags"`
	DefaultPostCategories *[]string `json:"default_post_categories"`
}
type Invite struct {
	RelatedUserID string `json:"related_user_id"`
	Role          Role   `json:"role"`
}
type Entitlement struct {
	Total            int64
	Level, PerkLevel int
}
type Mutation string

const (
	InviteMember   Mutation = "invite"
	AcceptInvite   Mutation = "accept"
	DeclineInvite  Mutation = "decline"
	RemoveMember   Mutation = "remove"
	LeavePublisher Mutation = "leave"
	UpdateRole     Mutation = "role"
)

type MemberChange struct {
	Action    Mutation
	AccountID string
	Role      Role
}

type Repository interface {
	LookupAccount(context.Context, string) (Account, error)
	UpdateProfile(context.Context, string, ProfilePatch) (Account, error)
	LookupPublisher(context.Context, string) (Publisher, error)
	ListPublishers(context.Context, string) ([]Publisher, error)
	ListInvites(context.Context, string) ([]Member, error)
	ListMembers(context.Context, string, int, int) ([]Member, int64, error)
	Membership(context.Context, string, string) (Member, error)
	CreatePublisher(context.Context, string, PublisherPatch) (Publisher, error)
	UpdatePublisher(context.Context, string, string, PublisherPatch) (Publisher, error)
	DeletePublisher(context.Context, string, string) error
	ChangeMember(context.Context, string, string, MemberChange) (Member, error)
	Resolve(context.Context, string, string) (string, error)
	RequireManager(context.Context, string, string) error
	PublisherEntitlement(context.Context, string) (Entitlement, error)
}

// An adapter must verify proof AND re-read authority state on every call.
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}
