// SPDX-License-Identifier: AGPL-3.0-only
package interactions

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid                        = &Failure{"INTERACTION_INVALID_REQUEST", "Invalid interaction request.", 400}
	ErrNotFound                       = &Failure{"INTERACTION_NOT_FOUND", "The requested resource does not exist.", 404}
	ErrCollectionNotFound             = &Failure{"COLLECTION_NOT_FOUND", "Collection not found.", 404}
	ErrDuplicate                      = &Failure{"COLLECTION_ADD_POST_FAILED", "Post already exists in this collection.", 400}
	ErrCollectionConflict             = &Failure{"COLLECTION_CREATE_FAILED", "A collection with this slug already exists.", 400}
	ErrOrder                          = &Failure{"COLLECTION_ORDER_NOT_SUPPORTED", "Manual ordering is not supported. Collection posts are sorted automatically by published date descending.", 400}
	ErrReorder                        = &Failure{"COLLECTION_REORDER_NOT_SUPPORTED", "Manual reordering is not supported. Collection posts are sorted automatically by published date descending.", 400}
	ErrUnavailable                    = &Failure{"INTERACTIONS_UNAVAILABLE", "The interaction dependency is unavailable.", 503}
	ErrUnsupported                    = &Failure{"INTERACTION_FEATURE_UNSUPPORTED", "This feature requires an unavailable provider.", 501}
	ErrCollectionSubscriptionNotFound = &Failure{"COLLECTION_SUBSCRIPTION_NOT_FOUND", "Subscription not found.", 404}
	ErrNextCollectionPost             = &Failure{"COLLECTION_POST_NOT_FOUND", "No next post found.", 404}
	ErrPrevCollectionPost             = &Failure{"COLLECTION_POST_NOT_FOUND", "No previous post found.", 404}
	ErrTagUnavailable                 = &Failure{"POST_TAG_UNAVAILABLE", "A requested tag is protected, expired or unavailable.", 400}
)

// Wire attitude values are explicitly assigned in the pinned authored Solian
// post_reaction_sheet.dart. They are not inferred from an omitted SDK enum.
const (
	Positive = 0
	Neutral  = 1
	Negative = 2
)

type ReactionRequest struct {
	Symbol   string `json:"symbol"`
	Attitude int    `json:"attitude"`
}
type Reaction struct {
	ID        string            `json:"id"`
	PostID    string            `json:"post_id"`
	AccountID string            `json:"account_id"`
	Symbol    string            `json:"symbol"`
	Attitude  int               `json:"attitude"`
	CreatedAt time.Time         `json:"created_at"`
	Account   *accounts.Account `json:"account,omitempty"`
}
type Bookmark struct {
	ID        string    `json:"id"`
	PostID    string    `json:"post_id"`
	AccountID string    `json:"account_id"`
	CreatedAt time.Time `json:"created_at"`
}
type CollectionSubscription = taxonomy.Subscription
type Collection = posts.Collection
type CollectionRequest struct {
	Slug         string  `json:"slug"`
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	BackgroundID *string `json:"background_id"`
	IconID       *string `json:"icon_id"`
}
type Page struct {
	Offset, Take         int
	Symbol, Order, Query string
}
type Repository interface {
	React(context.Context, string, string, ReactionRequest) (Reaction, bool, error)
	Reactions(context.Context, string, string, Page) ([]Reaction, int64, error)
	Bookmark(context.Context, string, string) (Bookmark, error)
	Unbookmark(context.Context, string, string) error
	BookmarkStatus(context.Context, string, string) (*Bookmark, error)
	Bookmarks(context.Context, string, Page) ([]posts.Post, int64, error)
	CreateCollection(context.Context, string, string, CollectionRequest) (Collection, error)
	UpdateCollection(context.Context, string, string, string, CollectionRequest) (Collection, error)
	DeleteCollection(context.Context, string, string, string) error
	Collection(context.Context, string, string, string) (Collection, error)
	Collections(context.Context, string, string, Page) ([]Collection, int64, error)
	AddCollectionPosts(context.Context, string, string, string, []string, bool) error
	RemoveCollectionPosts(context.Context, string, string, string, []string) error
	CollectionPosts(context.Context, string, string, string, Page) ([]posts.Post, int64, error)
	SubscribeCollection(context.Context, string, string, string) (CollectionSubscription, bool, error)
	UnsubscribeCollection(context.Context, string, string, string) error
	CollectionSubscription(context.Context, string, string, string) (CollectionSubscription, error)
	AdjacentCollectionPost(context.Context, string, string, string, string, bool) (posts.Post, error)
}

func ValidPage(p Page) bool {
	return p.Offset >= 0 && p.Take > 0 && p.Take <= 100 && len(p.Query) <= 512 && len(p.Symbol) <= 1024
}
func ValidateReaction(v ReactionRequest) error {
	if !utf8.ValidString(v.Symbol) || strings.ContainsRune(v.Symbol, 0) || taxonomy.UTF16Length(v.Symbol) > 256 || v.Attitude < 0 || v.Attitude > 2 {
		return ErrInvalid
	}
	switch v.Symbol {
	case "thumb_up", "thumb_down", "cry", "confuse", "hello", "shock", "speechless", "salute", "ridicule", "clap", "laugh", "angry", "party", "pray", "heart":
		return nil
	}
	if v.Symbol == "" {
		return ErrInvalid
	}
	return ErrUnsupported
}
func ValidateCollection(v CollectionRequest, create bool) error {
	if v.BackgroundID != nil || v.IconID != nil {
		return ErrUnsupported
	}
	if create && (strings.TrimSpace(v.Slug) == "" || taxonomy.UTF16Length(v.Slug) > 128 || strings.ContainsAny(v.Slug, "/\\?#") || !utf8.ValidString(v.Slug) || strings.ContainsRune(v.Slug, 0)) {
		return ErrInvalid
	}
	for _, s := range []struct {
		v *string
		n int
	}{{v.Name, 256}, {v.Description, 4096}} {
		if s.v != nil && (!utf8.ValidString(*s.v) || strings.ContainsRune(*s.v, 0) || taxonomy.UTF16Length(*s.v) > s.n) {
			return ErrInvalid
		}
	}
	return nil
}
func NormalizeError(e error) error {
	var f *Failure
	var a *authority.Failure
	var p *posts.Failure
	var c *accounts.Failure
	if e == nil || errors.As(e, &f) || errors.As(e, &a) || errors.As(e, &p) || errors.As(e, &c) {
		return e
	}
	return ErrUnavailable
}
