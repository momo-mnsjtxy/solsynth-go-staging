// SPDX-License-Identifier: AGPL-3.0-only
package history

import (
	"context"
	"math"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/presence"
	"strings"
	"time"
	"unicode/utf8"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"VALIDATION_ERROR", "Invalid account history request.", 400}
	ErrNotFound    = &Failure{"PASSPORT_ACCOUNT_NOT_FOUND", "Account not found.", 404}
	ErrUnavailable = &Failure{"ACCOUNT_HISTORY_UNAVAILABLE", "Account history is unavailable.", 503}
)

// From is inclusive and To exclusive. Stable ordering is created_at DESC, kind, id.
type Filter struct {
	Take, Offset int
	From, To     *time.Time
	Action       string
}

func Validate(f Filter) error {
	if f.Take < 1 || f.Take > 200 || f.Offset < 0 || f.Offset > 100000 || len(f.Action) > 256 || !utf8.ValidString(f.Action) || strings.ContainsRune(f.Action, 0) || f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return ErrInvalid
	}
	return nil
}

type TimelineItem struct {
	ID        string               `json:"id"`
	CreatedAt time.Time            `json:"created_at"`
	EventType int                  `json:"event_type"`
	Status    *presence.Status     `json:"status"`
	Activity  *activities.Activity `json:"activity"`
}
type ActionLog struct {
	ID        string         `json:"id"`
	AccountID string         `json:"account_id"`
	Action    string         `json:"action"`
	Meta      map[string]any `json:"meta"`
	UserAgent *string        `json:"user_agent"`
	IPAddress *string        `json:"ip_address"`
	Location  any            `json:"location"`
	SessionID *string        `json:"session_id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at"`
}
type CreditRecord struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	ReasonType string     `json:"reason_type"`
	Reason     string     `json:"reason"`
	Delta      float64    `json:"delta"`
	ExpiredAt  *time.Time `json:"expired_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at"`
	Status     int        `json:"status"`
}

// EffectiveDelta transcribes the authored SocialCreditValidationJob, including
// its future-created record behavior. This is not a guessed Shared model method.
func EffectiveDelta(v CreditRecord, now time.Time) (float64, error) {
	if math.IsNaN(v.Delta) || math.IsInf(v.Delta, 0) {
		return 0, ErrUnavailable
	}
	if v.ExpiredAt == nil {
		return v.Delta, nil
	}
	if !v.ExpiredAt.After(now) {
		return 0, nil
	}
	total := secondsBetween(*v.ExpiredAt, v.CreatedAt)
	if total == 0 {
		return v.Delta, nil
	}
	result := v.Delta * math.Max(0, 1-secondsBetween(now, v.CreatedAt)/total)
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, ErrUnavailable
	}
	return result, nil
}

const BaseSocialCredit = 100.0

type Connection struct {
	ID                 string            `json:"id"`
	AccountID          string            `json:"account_id"`
	Provider           string            `json:"provider"`
	ProvidedIdentifier string            `json:"provided_identifier"`
	Meta               map[string]string `json:"meta"`
	IsPublic           bool              `json:"is_public"`
	LastUsedAt         time.Time         `json:"last_used_at"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}
type TimelineRepository interface {
	Timeline(context.Context, string, string, Filter) ([]TimelineItem, int, error)
}
type Repository interface {
	TimelineRepository
	Actions(context.Context, string, Filter) ([]ActionLog, int, error)
	Credits(context.Context, string, string) (float64, error)
	CreditHistory(context.Context, string, Filter) ([]CreditRecord, int, error)
	Connections(context.Context, string, string) ([]Connection, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}

func secondsBetween(a, b time.Time) float64 {
	return float64(a.Unix()-b.Unix()) + float64(a.Nanosecond()-b.Nanosecond())/1e9
}
