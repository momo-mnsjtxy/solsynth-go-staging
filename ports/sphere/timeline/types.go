// SPDX-License-Identifier: AGPL-3.0-only
// Source and independent policy boundaries: docs/timeline-continuation.md.
package timeline

import (
	"context"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/posts"
	"time"
)

type Options struct {
	Take                                 int
	Cursor                               string
	Mode                                 sphere.Mode
	Filter, Publisher, Collection, Realm string
	Aggressive                           bool
}
type SearchOptions struct {
	Offset, Take                   int
	Publisher, Query, Order, Realm string
	Type                           *int
	Replies                        *bool
	Tags, Categories               []string
	Start, End                     *time.Time
	Ascending                      bool
}
type Event struct {
	ID                 string      `json:"id"`
	ResourceIdentifier string      `json:"resource_identifier"`
	UpdatedAt          time.Time   `json:"updated_at"`
	DeletedAt          *time.Time  `json:"deleted_at"`
	Type               string      `json:"type"`
	CreatedAt          time.Time   `json:"created_at"`
	Data               *posts.Post `json:"data"`
}
type Page struct {
	Items      []Event     `json:"items"`
	NextCursor *string     `json:"next_cursor"`
	Mode       sphere.Mode `json:"mode"`
}
type ThreadNode struct {
	Post     posts.Post `json:"post"`
	Depth    int        `json:"depth"`
	ParentID *string    `json:"parent_id"`
}
type Thread struct {
	Ancestors   []ThreadNode `json:"ancestors"`
	Current     ThreadNode   `json:"current"`
	Descendants []ThreadNode `json:"descendants"`
	HasMore     bool         `json:"has_more"`
}
type ThreadOptions struct {
	Take, AncestorLimit int
	Ancestors           bool
}

type Subscription struct {
	Publisher        *accounts.Publisher `json:"publisher,omitempty"`
	IsActive         bool                `json:"is_active"`
	IsPendingRequest bool                `json:"is_pending_request"`
	ID               string              `json:"id"`
	AccountID        string              `json:"account_id"`
	PublisherID      string              `json:"publisher_id"`
	State            string              `json:"state"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
	EndedAt          *time.Time          `json:"ended_at"`
	Reason           *string             `json:"reason"`
	Notify           bool                `json:"notify"`
	LastReadAt       *time.Time          `json:"last_read_at"`
}
type SubscriptionStatus struct {
	Subscription     *Subscription `json:"subscription"`
	FollowRequest    *Subscription `json:"follow_request"`
	RequiresApproval bool          `json:"requires_approval"`
	Status           string        `json:"status"`
	Message          string        `json:"message"`
	IsPending        bool          `json:"is_pending"`
	IsActive         bool          `json:"is_active"`
	Notify           bool          `json:"notify"`
}
type SubscriptionItem struct {
	Subscription    `json:"subscription"`
	LastReadAt      *time.Time `json:"last_read_at"`
	LatestContentAt *time.Time `json:"latest_content_at"`
	HasNewContent   bool       `json:"has_new_content"`
	IsLive          bool       `json:"is_live"`
}
type Preference struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	Kind        string    `json:"kind"`
	ReferenceID string    `json:"reference_id"`
	Reason      *string   `json:"reason"`
	AppliedAt   time.Time `json:"applied_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Interest struct {
	Kind             string    `json:"kind"`
	ReferenceID      string    `json:"reference_id"`
	Label            string    `json:"label"`
	Score            float64   `json:"score"`
	InteractionCount int64     `json:"interaction_count"`
	LastInteractedAt time.Time `json:"last_interacted_at"`
	LastSignalType   string    `json:"last_signal_type"`
}
type Suggestion struct {
	Kind        string   `json:"kind"`
	ReferenceID string   `json:"reference_id"`
	Label       string   `json:"label"`
	Score       float64  `json:"score"`
	Reasons     []string `json:"reasons"`
	Data        any      `json:"data"`
}
type Profile struct {
	GeneratedAt         time.Time    `json:"generated_at"`
	Interests           []Interest   `json:"interests"`
	SuggestedPublishers []Suggestion `json:"suggested_publishers"`
	SuggestedAccounts   []Suggestion `json:"suggested_accounts"`
	SuggestedRealms     []Suggestion `json:"suggested_realms"`
	Suppressed          []Suggestion `json:"suppressed"`
}
type PreferenceRequest struct {
	Kind        string  `json:"kind"`
	ReferenceID string  `json:"reference_id"`
	Reason      *string `json:"reason"`
}
type WeightRequest struct {
	Kind             string  `json:"kind"`
	ReferenceID      string  `json:"reference_id"`
	ScoreDelta       float64 `json:"score_delta"`
	InteractionCount int     `json:"interaction_count"`
	SignalType       string  `json:"signal_type"`
}
type FeedbackRequest struct {
	Kind        string  `json:"kind"`
	ReferenceID string  `json:"reference_id"`
	Feedback    string  `json:"feedback"`
	Reason      *string `json:"reason"`
	Suppress    bool    `json:"suppress"`
}
type FeedbackResult struct {
	UpdatedProfiles []Interest  `json:"updated_profiles"`
	Preference      *Preference `json:"preference"`
}
type Repository interface {
	WatchPreferences(context.Context, string) ([]sphere.WatchPreference, error)
	PatchWatch(context.Context, string, []sphere.WatchPreferenceUpdate) ([]sphere.WatchPreference, error)
	Feed(context.Context, string, Options) (Page, error)
	Search(context.Context, string, SearchOptions) ([]posts.Post, int64, error)
	Related(context.Context, string, string, string, SearchOptions) ([]posts.Post, int64, error)
	Thread(context.Context, string, string, ThreadOptions) (Thread, error)
	ThreadedReplies(context.Context, string, string, SearchOptions) ([]ThreadNode, int64, error)
	Chain(context.Context, string, string) ([]posts.Post, error)
	SubscriptionStatus(context.Context, string, string) (SubscriptionStatus, error)
	Subscribe(context.Context, string, string) (SubscriptionStatus, error)
	Unsubscribe(context.Context, string, string) (SubscriptionStatus, error)
	Subscriptions(context.Context, string, int, int) ([]SubscriptionItem, int64, error)
	FollowRequests(context.Context, string, string) ([]Subscription, error)
	DecideFollow(context.Context, string, string, string, bool, *string) (Subscription, error)
	Discovery(context.Context, string) (Profile, error)
	Suppress(context.Context, string, PreferenceRequest) (Preference, error)
	Unsuppress(context.Context, string, string, string) error
	AdjustWeight(context.Context, string, WeightRequest) (Interest, error)
	Feedback(context.Context, string, FeedbackRequest) (FeedbackResult, error)
	ResetInterests(context.Context, string) (int64, error)
}

var (
	ErrPublisherNotFound  = &posts.Failure{Code: "PUBLISHER_NOT_FOUND", Message: "Publisher not found.", Status: 404}
	ErrCollectionNotFound = &posts.Failure{Code: "COLLECTION_NOT_FOUND", Message: "Collection not found.", Status: 404}
	ErrInvalid            = &posts.Failure{Code: "TIMELINE_INVALID_REQUEST", Message: "Invalid timeline request.", Status: 400}
	ErrCursor             = &posts.Failure{Code: "TIMELINE_INVALID_CURSOR", Message: "Invalid cursor format.", Status: 400}
	ErrMode               = &posts.Failure{Code: "TIMELINE_INVALID_MODE", Message: "Invalid mode. Expected personalized, top, or latest.", Status: 400}
	ErrScope              = &posts.Failure{Code: "TIMELINE_INVALID_SCOPE", Message: "Collection timeline scope requires a publisher.", Status: 400}
	ErrDepth              = &posts.Failure{Code: "POST_THREAD_DEPTH_EXCEEDED", Message: "The thread exceeds the supported depth.", Status: 400}
)
