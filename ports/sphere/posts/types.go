// SPDX-License-Identifier: AGPL-3.0-only
// Bounded text-post implementation. Provenance and deliberate differences are
// documented in docs/post-lifecycle-continuation.md.
package posts

import (
	"context"
	"errors"
	"net/http"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/postmedia"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrNotFound        = &Failure{"POST_NOT_FOUND", "Post not found.", 404}
	ErrInvalid         = &Failure{"POST_INVALID_REQUEST", "Invalid post request.", 400}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrForbidden       = &Failure{"FORBIDDEN", "Publisher editor permission is required.", 403}
	ErrUnsupported     = &Failure{"POST_FEATURE_UNSUPPORTED", "This feature requires an unavailable provider.", 501}
	ErrConflict        = &Failure{"POST_CONFLICT", "The post conflicts with existing state.", 409}
	ErrNotDraft        = &Failure{"POST_NOT_A_DRAFT", "This post is not a draft.", 400}
	ErrLocked          = &Failure{"POST_LOCKED", "This post is locked.", 423}
	ErrUnavailable     = &Failure{"POSTS_UNAVAILABLE", "The post dependency is unavailable.", 503}
)

// These wire values are observed in the licensed Solian composer at the pinned
// revision, not copied from the excluded Shared model package.
const (
	Public       = 0
	Friends      = 1
	Unlisted     = 2
	Private      = 3
	CloseFriends = 4
	QuietPublic  = 5
)
const (
	Moment  = 0
	Article = 1
	Blog    = 2
)

type Collection struct {
	ID          string             `json:"id"`
	PublisherID string             `json:"publisher_id"`
	Publisher   accounts.Publisher `json:"publisher"`
	Slug        string             `json:"slug"`
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	Background  any                `json:"background"`
	Icon        any                `json:"icon"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	DeletedAt   *time.Time         `json:"deleted_at"`
}

type Post struct {
	Meta                 *Metadata                     `json:"meta,omitempty"`
	Schedule             *Schedule                     `json:"schedule,omitempty"`
	Automod              *AutomodDecision              `json:"automod,omitempty"`
	AwardedScore         wallet.Amount                 `json:"awarded_score"`
	Attachments          []postmedia.Attachment        `json:"attachments"`
	RepliedPostID        *string                       `json:"replied_post_id"`
	ForwardedPostID      *string                       `json:"forwarded_post_id"`
	ChainedPostID        *string                       `json:"chained_post_id"`
	RepliedPost          *Post                         `json:"replied_post"`
	ForwardedPost        *Post                         `json:"forwarded_post"`
	ChainedPost          *Post                         `json:"chained_post"`
	RepliesCount         int64                         `json:"replies_count"`
	ForwardsCount        int64                         `json:"forwards_count"`
	ThreadRepliesCount   int64                         `json:"thread_replies_count"`
	ChainedCount         int64                         `json:"chained_count"`
	ChainedPosts         []Post                        `json:"chained_posts"`
	Tags                 []taxonomy.TagProjection      `json:"tags"`
	Categories           []taxonomy.CategoryProjection `json:"categories"`
	PublisherCollections []Collection                  `json:"publisher_collections"`
	ReactionsCount       map[string]int64              `json:"reactions_count"`
	ReactionsMade        map[string]bool               `json:"reactions_made"`
	ID                   string                        `json:"id"`
	Title                *string                       `json:"title"`
	Description          *string                       `json:"description"`
	Slug                 *string                       `json:"slug"`
	Content              string                        `json:"content"`
	Visibility           int                           `json:"visibility"`
	Type                 int                           `json:"type"`
	Language             *string                       `json:"language"`
	PublisherID          string                        `json:"publisher_id"`
	RealmID              *string                       `json:"realm_id,omitempty"`
	Publisher            accounts.Publisher            `json:"publisher"`
	DraftedAt            *time.Time                    `json:"drafted_at"`
	PublishedAt          *time.Time                    `json:"published_at"`
	CreatedAt            time.Time                     `json:"created_at"`
	UpdatedAt            time.Time                     `json:"updated_at"`
	DeletedAt            *time.Time                    `json:"deleted_at"`
	LockedAt             *time.Time                    `json:"locked_at"`
}
type Patch struct {
	BlogEmbeds      bool       `json:"-"`
	RealmID         *string    `json:"realm_id,omitempty"`
	ClearRealm      bool       `json:"-"`
	Attachments     *[]string  `json:"attachments"`
	RepliedPostID   *string    `json:"replied_post_id"`
	ForwardedPostID *string    `json:"forwarded_post_id"`
	ChainedPostID   *string    `json:"chained_post_id"`
	Tags            *[]string  `json:"tags"`
	Categories      *[]string  `json:"categories"`
	CollectionIDs   *[]string  `json:"collection_ids"`
	Title           *string    `json:"title"`
	Description     *string    `json:"description"`
	Slug            *string    `json:"slug"`
	Content         *string    `json:"content"`
	Visibility      *int       `json:"visibility"`
	Type            *int       `json:"type"`
	Language        *string    `json:"language"`
	DraftedAt       *time.Time `json:"drafted_at"`
	PublishedAt     *time.Time `json:"published_at"`
}
type ListOptions struct {
	Offset, Take int
	Publisher    string
	Realm        string
	Type         *int
	Drafts       bool
	Ascending    bool
}
type Repository interface {
	Create(context.Context, string, string, Patch) (Post, error)
	Update(context.Context, string, string, Patch) (Post, error)
	Publish(context.Context, string, string) (Post, error)
	Delete(context.Context, string, string) error
	Get(context.Context, string, string) (Post, error)
	GetSlug(context.Context, string, string, string) (Post, error)
	List(context.Context, string, ListOptions) ([]Post, int64, error)
}

// Empty identity with nil error is permitted for public reads only. The host
// adapter must validate trusted proof and fresh authority state, never headers
// containing an asserted account ID. Authorize must revalidate the same proof.
type Auth interface {
	Authenticate(*http.Request) (string, error)
	Authorize(*http.Request, string) error
}

func ValidatePatch(p Patch, create bool, now time.Time) error {
	if p.ClearRealm && p.RealmID != nil || p.RealmID != nil && !authority.ValidID(*p.RealmID) {
		return ErrInvalid
	}
	if p.Attachments != nil {
		if _, ok := postmedia.NormalizeIDs(*p.Attachments); !ok {
			return ErrInvalid
		}
	}
	for _, id := range []*string{p.RepliedPostID, p.ForwardedPostID, p.ChainedPostID} {
		if id != nil && !authority.ValidID(*id) {
			return ErrInvalid
		}
	}
	if p.ChainedPostID != nil && (p.RepliedPostID != nil || p.ForwardedPostID != nil) {
		return &Failure{"POST_CHAIN_CONFLICT", "A chained post cannot also be a reply or a forward.", 400}
	}
	for _, f := range []struct {
		values *[]string
		max    int
	}{{p.Tags, 16}, {p.Categories, 8}, {p.CollectionIDs, 16}} {
		if f.values != nil {
			if len(*f.values) > f.max {
				return ErrInvalid
			}
			for _, v := range *f.values {
				if !utf8.ValidString(v) || strings.ContainsRune(v, 0) || taxonomy.UTF16Length(v) > 128 || strings.TrimSpace(v) == "" {
					return ErrInvalid
				}
			}
		}
	}
	if p.CollectionIDs != nil {
		for _, id := range *p.CollectionIDs {
			if !authority.ValidID(id) {
				return ErrInvalid
			}
		}
	}

	for _, f := range []struct {
		value *string
		max   int
	}{{p.Title, 1024}, {p.Description, 4096}, {p.Slug, 1024}, {p.Language, 16}, {p.Content, 1 << 20}} {
		if f.value != nil && (!utf8.ValidString(*f.value) || strings.ContainsRune(*f.value, 0) || utf8.RuneCountInString(*f.value) > f.max) {
			return ErrInvalid
		}
	}
	if create && (p.Content == nil || strings.TrimSpace(*p.Content) == "") && (p.Attachments == nil || len(*p.Attachments) == 0) {
		return ErrInvalid
	}
	if p.Visibility != nil && *p.Visibility != Public && *p.Visibility != Unlisted && *p.Visibility != Private {
		return ErrUnsupported
	}
	if p.Type != nil && *p.Type != Moment && *p.Type != Article {
		return ErrUnsupported
	}
	if p.DraftedAt != nil && p.PublishedAt != nil {
		return &Failure{"POST_DRAFT_PUBLISH_CONFLICT", "Cannot set both drafted_at and published_at.", 400}
	}
	if p.PublishedAt != nil && p.PublishedAt.After(now) {
		return ErrUnsupported
	}
	if p.DraftedAt != nil && p.DraftedAt.IsZero() || p.PublishedAt != nil && p.PublishedAt.IsZero() {
		return ErrInvalid
	}
	if p.Slug != nil && strings.ContainsAny(*p.Slug, "/?#") {
		return ErrInvalid
	}
	return nil
}
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
	var blog *blogs.Failure
	if errors.As(e, &blog) {
		return e
	}
	var p *accounts.Failure
	if errors.As(e, &p) {
		return e
	}
	return ErrUnavailable
}
