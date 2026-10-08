// SPDX-License-Identifier: AGPL-3.0-only
package sponsorship

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/sphere/posts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

const Product = "ads.sponsor"

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"SPONSOR_INVALID", "Invalid sponsorship request.", 400}
	ErrNotFound    = &Failure{"SPONSOR_NOT_FOUND", "Sponsorship content not found.", 404}
	ErrForbidden   = &Failure{"SPONSOR_FORBIDDEN", "Sponsorship access denied.", 403}
	ErrUnavailable = &Failure{"SPONSOR_UNAVAILABLE", "Sponsorship is unavailable.", 503}
	ErrRetry       = &Failure{"SPONSOR_RETRY_REQUIRED", "The transaction rolled back after contention; retry the same request.", 409}
	ErrConflict    = &Failure{"SPONSOR_CONFLICT", "Sponsorship state changed; retry the request.", 409}
)

func Minimum() wallet.Amount { v, _ := wallet.ParseAmount("5"); return v }

type Request struct {
	Amount wallet.Amount `json:"amount"`
}
type OrderResponse struct {
	OrderID string        `json:"order_id"`
	Amount  wallet.Amount `json:"amount"`
}
type Bid struct {
	UpdatedAt time.Time     `json:"updated_at"`
	DeletedAt *time.Time    `json:"deleted_at"`
	ID        string        `json:"id"`
	PostID    string        `json:"post_id"`
	AccountID string        `json:"account_id"`
	Amount    wallet.Amount `json:"amount"`
	ExpiresAt time.Time     `json:"expires_at"`
	CreatedAt time.Time     `json:"created_at"`
}
type Entry struct {
	PostID      string        `json:"post_id"`
	TotalAmount wallet.Amount `json:"total_amount"`
	BidCount    int64         `json:"bid_count"`
}
type Ad struct {
	PostID            string        `json:"post_id"`
	Title             *string       `json:"title"`
	Slug              *string       `json:"slug"`
	ActiveBidTotal    wallet.Amount `json:"active_bid_total"`
	BidCount          int64         `json:"bid_count"`
	IsCurrentlyPlaced bool          `json:"is_currently_placed"`
	ShownCount        int64         `json:"shown_count"`
	LastShownAt       *time.Time    `json:"last_shown_at"`
	DisplayChance     *float64      `json:"display_chance,omitempty"`
}
type SponsoredPost struct {
	posts.Post
	Sponsored bool `json:"sponsored"`
}
type Current struct {
	Sponsored bool           `json:"sponsored"`
	Post      *SponsoredPost `json:"post,omitempty"`
}
type Page struct{ Take, Offset int }

func (q Page) Bounds() Page {
	if q.Take <= 0 {
		q.Take = 20
	}
	if q.Take > 100 {
		q.Take = 100
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Offset > 10000 {
		q.Offset = 10000
	}
	return q
}

type Repository interface {
	CreateOrder(context.Context, string, string, string, Request) (OrderResponse, error)
	Total(context.Context, string, string) (wallet.Amount, error)
	History(context.Context, string, string, Page) ([]Bid, int, error)
	Leaderboard(context.Context, string, Page) ([]Entry, int, error)
	Current(context.Context, string) (Current, error)
	Ads(context.Context, string, string, bool, Page) ([]Ad, int, error)
}
type Auth func(*http.Request, bool) (string, error)
