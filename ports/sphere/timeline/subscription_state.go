// SPDX-License-Identifier: AGPL-3.0-only
package timeline

import (
	"context"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"solsynth.local/go-migration/ports/sphere/posts"
	"time"
)

type SubscriptionReadStatus struct {
	Subscription    Subscription `json:"subscription"`
	LatestContentAt *time.Time   `json:"latest_content_at"`
	HasNewContent   bool         `json:"has_new_content"`
}
type Subscriber struct {
	Subscription Subscription      `json:"subscription"`
	Account      *accounts.Account `json:"account"`
}
type SubscriptionStateRepository interface {
	SubscriptionReadStatus(context.Context, string, string) (SubscriptionReadStatus, error)
	UpdateSubscriptionReadStatus(context.Context, string, string, *time.Time) (SubscriptionReadStatus, error)
	MarkAllSubscriptionsRead(context.Context, string) (int, error)
	UpdateSubscriptionNotify(context.Context, string, string, bool) (Subscription, error)
	Subscribers(context.Context, string, string, int, int) ([]Subscriber, int64, error)
	AddSubscriber(context.Context, string, string, string) (Subscriber, error)
	RemoveSubscriber(context.Context, string, string, string) error
	SubscribedLiveStreams(context.Context, string, int, int) ([]live.Stream, error)
}

var ErrSubscriptionNotFound = &posts.Failure{Code: "PUBLISHER_SUBSCRIPTION_NOT_FOUND", Message: "Subscription not found.", Status: 404}
var ErrSubscriberAdd = &posts.Failure{Code: "PUBLISHER_SUBSCRIBER_ADD_FAILED", Message: "The account already has a subscription or has previously left or been removed.", Status: 400}
