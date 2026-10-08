// SPDX-License-Identifier: AGPL-3.0-only
package content

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid       = &Failure{"MODERATION_INVALID", "Invalid moderation request.", 400}
	ErrForbidden     = &Failure{"MODERATION_FORBIDDEN", "Current global administration or scoped moderation authority is required.", 403}
	ErrNotFound      = &Failure{"MODERATION_NOT_FOUND", "The requested resource is unavailable.", 404}
	ErrConflict      = &Failure{"MODERATION_CONFLICT", "The resource changed or the transition violates its current state.", 409}
	ErrUnavailable   = &Failure{"MODERATION_UNAVAILABLE", "Moderation dependencies are unavailable.", 503}
	ErrUnsupported   = &Failure{"MODERATION_UNSUPPORTED", "This moderation operation is not implemented.", 501}
	ErrAlreadyLocked = &Failure{"POST_ALREADY_LOCKED", "Post is already locked.", 400}
	ErrNotLocked     = &Failure{"POST_NOT_LOCKED", "Post is not locked.", 400}
	ErrNotInRealm    = &Failure{"POST_NOT_IN_REALM", "The post is not linked to a realm.", 400}
)

type Kind string

const (
	PostKind      Kind = "post"
	PublisherKind Kind = "publisher"
	RealmKind     Kind = "realm"
)

func (k Kind) Permission(action string) string {
	switch k {
	case PostKind:
		switch action {
		case "lock", "unlock":
			return "posts.lock"
		case "delete":
			return "posts.delete"
		default:
			return "posts.moderate"
		}
	case PublisherKind:
		switch action {
		case "update":
			return "publishers.update"
		case "delete":
			return "publishers.delete"
		default:
			return "publishers.moderate"
		}
	case RealmKind:
		switch action {
		case "update":
			return "realms.update"
		case "delete":
			return "realms.delete"
		case "role", "remove_member":
			return "realms.members.manage"
		default:
			return "realms.moderate"
		}
	}
	return ""
}
func ValidReason(v string, none bool) bool {
	switch v {
	case "spam", "advertising", "harassment", "hate_speech", "misinformation", "illegal", "other":
		return true
	case "none":
		return none
	}
	return false
}

type State struct {
	ShadowbanReason string     `json:"shadowban_reason"`
	ShadowbannedAt  *time.Time `json:"shadowbanned_at"`
}
type Post struct {
	posts.Post
	State
}
type Publisher struct {
	accounts.Publisher
	State
}
type PublisherDetail struct {
	Publisher       Publisher `json:"publisher"`
	MemberCount     int64     `json:"member_count"`
	PostCount       int64     `json:"post_count"`
	CollectionCount int64     `json:"collection_count"`
	SubscriberCount int64     `json:"subscriber_count"`
}
type RealmDetail struct {
	Realm              social.Realm `json:"realm"`
	MemberCount        int64        `json:"member_count"`
	PendingInviteCount int64        `json:"pending_invite_count"`
}
type Query struct {
	Text, PublisherID, RealmID, AccountID                         string
	Visibility, Type, Role                                        *int
	Reason                                                        *string
	Banned, Locked, Drafted, Public, Community, Pending, Verified *bool
	Take, Offset                                                  int
}
type PublisherPatch struct {
	Name                 *string `json:"name"`
	Nick                 *string `json:"nick"`
	Bio                  *string `json:"bio"`
	GatekeptFollows      *bool   `json:"gatekept_follows"`
	ModerateSubscription *bool   `json:"moderate_subscription"`
}
type RealmPatch struct {
	Slug        *string `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	IsPublic    *bool   `json:"is_public"`
	IsCommunity *bool   `json:"is_community"`
	AccountID   *string `json:"account_id"`
}
type Command struct {
	Action     string
	IDs        []string
	Reason     *string
	Visibility *int
	Publisher  *PublisherPatch
	Realm      *RealmPatch
	AccountID  string
	Role       *int
}
type Repository interface {
	List(context.Context, string, Kind, Query) (any, int64, error)
	Get(context.Context, string, Kind, string) (any, error)
	Mutate(context.Context, string, Kind, string, Command) (any, error)
	Members(context.Context, string, string, Query) ([]social.Member, int64, error)
	ModerateRealmPost(context.Context, string, string, *string) (Post, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}
