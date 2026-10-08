// SPDX-License-Identifier: AGPL-3.0-only
// Owned, versioned survey projection; see docs/surveys-stickers-continuation.md.
package surveys

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"solsynth.local/go-migration/ports/media"
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
	ErrInvalid         = &Failure{"SURVEY_INVALID_REQUEST", "Invalid survey request.", 400}
	ErrValidation      = &Failure{"SURVEY_VALIDATION_FAILED", "Question or answer validation failed.", 422}
	ErrNotFound        = &Failure{"SURVEY_NOT_FOUND", "Survey not found.", 404}
	ErrForbidden       = &Failure{"SURVEY_FORBIDDEN", "Survey access is forbidden.", 403}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrConflict        = &Failure{"SURVEY_INVALID_STATE", "The survey does not accept this change in its current state.", 409}
	ErrUnavailable     = &Failure{"SURVEYS_UNAVAILABLE", "Survey dependency unavailable.", 503}
	ErrUnsupported     = &Failure{"SURVEY_FEATURE_UNSUPPORTED", "Requested survey feature is unavailable.", 501}
)

const (
	Draft          = 0
	Published      = 1
	Archived       = 2
	SingleChoice   = 0
	MultipleChoice = 1
	FreeText       = 4
	Rating         = 3
	YesNo          = 2
)

type Option struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description *string `json:"description"`
	Order       int     `json:"order"`
}
type Question struct {
	ID            string   `json:"id"`
	Type          int      `json:"type"`
	Title         string   `json:"title"`
	Description   *string  `json:"description"`
	Order         int      `json:"order"`
	Required      bool     `json:"is_required"`
	Options       []Option `json:"options"`
	MaxSelections *int     `json:"max_selections"`
	MaxLength     *int     `json:"max_length"`
	MinValue      *float64 `json:"min_value"`
	MaxValue      *float64 `json:"max_value"`
}
type Answer struct {
	ID        string                     `json:"id"`
	SurveyID  string                     `json:"survey_id"`
	AccountID *string                    `json:"account_id,omitempty"`
	Answer    map[string]json.RawMessage `json:"answer"`
	CreatedAt time.Time                  `json:"created_at"`
	UpdatedAt time.Time                  `json:"updated_at"`
}
type QuestionStats struct {
	Count   int64            `json:"count"`
	Choices map[string]int64 `json:"choices,omitempty"`
	Average *float64         `json:"average,omitempty"`
}
type Results struct {
	Total     int64                    `json:"total"`
	Questions map[string]QuestionStats `json:"questions"`
}
type Survey struct {
	ID          string                      `json:"id"`
	PublisherID string                      `json:"publisher_id"`
	Publisher   accounts.Publisher          `json:"publisher"`
	PostID      *string                     `json:"post_id"`
	Title       *string                     `json:"title"`
	Description *string                     `json:"description"`
	EndedAt     *time.Time                  `json:"ended_at"`
	Anonymous   bool                        `json:"is_anonymous"`
	HideResults bool                        `json:"hide_results"`
	Status      int                         `json:"status"`
	Questions   []Question                  `json:"questions"`
	Attachments []media.File                `json:"attachments"`
	CreatedAt   time.Time                   `json:"created_at"`
	UpdatedAt   time.Time                   `json:"updated_at"`
	PublishedAt *time.Time                  `json:"published_at"`
	UserAnswer  *Answer                     `json:"user_answer"`
	Stats       map[string]map[string]int64 `json:"stats"`
}
type Patch struct {
	Title             *string     `json:"title"`
	Description       *string     `json:"description"`
	EndedAt           *time.Time  `json:"ended_at"`
	ClearEndedAt      bool        `json:"clear_ended_at"`
	Anonymous         *bool       `json:"is_anonymous"`
	HideResults       *bool       `json:"hide_results"`
	PostID            *string     `json:"post_id"`
	Questions         *[]Question `json:"questions"`
	Attachments       *[]string   `json:"attachments"`
	NotifySubscribers *bool       `json:"notify_subscribers"`
}
type Page struct {
	Take, Offset int
	Publisher    string
	Active       bool
}
type Repository interface {
	Create(context.Context, string, string, Patch) (Survey, error)
	Update(context.Context, string, string, Patch) (Survey, error)
	Transition(context.Context, string, string, string) (Survey, error)
	Delete(context.Context, string, string) error
	Get(context.Context, string, string) (Survey, error)
	List(context.Context, string, Page) ([]Survey, int64, error)
	Answer(context.Context, string, string, map[string]json.RawMessage) (Answer, error)
	Withdraw(context.Context, string, string) error
	Results(context.Context, string, string) (Results, error)
	Feedback(context.Context, string, string, Page) ([]Answer, int64, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
	Authorize(*http.Request, string) error
}
type MediaResolver interface {
	ResolvePublicInTransaction(context.Context, *sql.Tx, string, string) (media.File, error)
}
