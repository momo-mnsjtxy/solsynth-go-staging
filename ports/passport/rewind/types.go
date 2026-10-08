// SPDX-License-Identifier: AGPL-3.0-only
package rewind

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"PASSPORT_REWIND_INVALID", "Invalid rewind request.", 400}
	ErrNotFound    = &Failure{"PASSPORT_REWIND_POINT_NOT_FOUND", "Rewind point not found.", 404}
	ErrUnavailable = &Failure{"PASSPORT_REWIND_UNAVAILABLE", "Rewind is unavailable.", 503}
)

func Normalize(e error) error {
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) || errors.As(e, &a) {
		return e
	}
	return ErrUnavailable
}

type Point struct {
	ID            string            `json:"id"`
	Year          int               `json:"year"`
	SchemaVersion int               `json:"schema_version"`
	SharableCode  *string           `json:"sharable_code"`
	Data          map[string]any    `json:"data"`
	AccountID     string            `json:"account_id"`
	Account       *accounts.Account `json:"account"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	// These fields explicitly distinguish incomplete owned components from an
	// original complete annual report. No absent provider is reported as zero.
	UnavailableComponents []string `json:"unavailable_components"`
	Projection            string   `json:"projection"`
}
type Word struct {
	Word  string `json:"word"`
	Count int64  `json:"count"`
}
type ProductiveDay struct {
	Date      string `json:"date"`
	PostCount int64  `json:"post_count"`
}
type Passport struct {
	MaxCheckInStreak    int     `json:"max_check_in_streak"`
	CheckInCompleteness float64 `json:"check_in_completeness"`
	MostActiveDay       *string `json:"most_active_day"`
	MostActiveWeekday   *string `json:"most_active_weekday"`
	LatestActiveTime    *string `json:"latest_active_time"`
	NewFriendsCount     int64   `json:"new_friends_count"`
	NewBlockedCount     int64   `json:"new_blocked_count"`
}
type Reference struct {
	ID      string `json:"id"`
	Count   int64  `json:"count"`
	ScopeID string `json:"scope_id,omitempty"`
}

// Snapshot stores only measured values and canonical IDs, never cached private
// account, publisher, post, room or provider projections.
type Snapshot struct {
	Passport               Passport       `json:"passport"`
	TotalPosts             int64          `json:"total_post_count"`
	TotalUpvotes           int64          `json:"total_upvote_count"`
	MostPopularPost        string         `json:"most_popular_post_id"`
	MostProductiveDay      *ProductiveDay `json:"most_productive_day"`
	MostLovedPublisher     *Reference     `json:"most_loved_publisher"`
	MostLovedAudience      *Reference     `json:"most_loved_audience"`
	TopWords               []Word         `json:"top_words"`
	MostMessagedChat       *Reference     `json:"most_messaged_chat"`
	MostMessagedDirectChat *Reference     `json:"most_messaged_direct_chat"`
	MostCalledChat         *Reference     `json:"most_called_chat"`
	MostCalledChatMembers  []string       `json:"most_called_chat_members"`
	MostCalledAccounts     []Reference    `json:"most_called_accounts"`
	Unavailable            []string       `json:"unavailable_components"`
}

func Window(year int) (time.Time, time.Time, error) {
	if year < 1901 || year > 2199 {
		return time.Time{}, time.Time{}, ErrInvalid
	}
	return time.Date(year-1, 12, 26, 0, 0, 0, 0, time.UTC), time.Date(year, 12, 26, 0, 0, 0, 0, time.UTC), nil
}
func CheckInMetrics(days []time.Time) (int, float64) {
	seen := map[string]bool{}
	for _, d := range days {
		seen[d.UTC().Format("2006-01-02")] = true
	}
	best := 0
	for key := range seen {
		d, _ := time.Parse("2006-01-02", key)
		if seen[d.AddDate(0, 0, -1).Format("2006-01-02")] {
			continue
		}
		n := 0
		for seen[d.Format("2006-01-02")] {
			n++
			d = d.AddDate(0, 0, 1)
		}
		if n > best {
			best = n
		}
	}
	return best, float64(len(seen)) / 365
}

type Repository interface {
	Current(context.Context, string) (Point, error)
	Public(context.Context, string) (Point, error)
	Share(context.Context, string, int, bool) (Point, error)
}
