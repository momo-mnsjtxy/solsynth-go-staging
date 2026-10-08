// SPDX-License-Identifier: AGPL-3.0-only
// Bounded text-post implementation. Provenance and deliberate differences are
// documented in docs/post-lifecycle-continuation.md.
package posts

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
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

type Post struct {
	ID          string             `json:"id"`
	Title       *string            `json:"title"`
	Description *string            `json:"description"`
	Slug        *string            `json:"slug"`
	Content     string             `json:"content"`
	Visibility  int                `json:"visibility"`
	Type        int                `json:"type"`
	Language    *string            `json:"language"`
	PublisherID string             `json:"publisher_id"`
	Publisher   accounts.Publisher `json:"publisher"`
	DraftedAt   *time.Time         `json:"drafted_at"`
	PublishedAt *time.Time         `json:"published_at"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	DeletedAt   *time.Time         `json:"deleted_at"`
	LockedAt    *time.Time         `json:"locked_at"`
}
type Patch struct {
	Title       *string    `json:"title"`
	Description *string    `json:"description"`
	Slug        *string    `json:"slug"`
	Content     *string    `json:"content"`
	Visibility  *int       `json:"visibility"`
	Type        *int       `json:"type"`
	Language    *string    `json:"language"`
	DraftedAt   *time.Time `json:"drafted_at"`
	PublishedAt *time.Time `json:"published_at"`
}
type ListOptions struct {
	Offset, Take int
	Publisher    string
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
	for _, f := range []struct {
		value *string
		max   int
	}{{p.Title, 1024}, {p.Description, 4096}, {p.Slug, 1024}, {p.Language, 16}, {p.Content, 1 << 20}} {
		if f.value != nil && (!utf8.ValidString(*f.value) || strings.ContainsRune(*f.value, 0) || utf8.RuneCountInString(*f.value) > f.max) {
			return ErrInvalid
		}
	}
	if create && (p.Content == nil || strings.TrimSpace(*p.Content) == "") || p.Content != nil && strings.TrimSpace(*p.Content) == "" {
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
	var p *accounts.Failure
	if errors.As(e, &p) {
		return e
	}
	return ErrUnavailable
}
