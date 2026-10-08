// SPDX-License-Identifier: AGPL-3.0-only
// Package tickets implements support cases and the separate authored Passport
// examination contract. Provenance and owned differences are in its source notes.
package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"PASSPORT_INVALID_REQUEST", "Invalid request.", 400}
	ErrNotFound    = &Failure{"PASSPORT_NOT_FOUND", "Resource not found.", 404}
	ErrForbidden   = &Failure{"FORBIDDEN", "Permission denied.", 403}
	ErrConflict    = &Failure{"PASSPORT_CONFLICT", "The request conflicts with current state.", 409}
	ErrLimit       = &Failure{"PASSPORT_LIMIT_REACHED", "The configured limit has been reached.", 400}
	ErrUnavailable = &Failure{"PASSPORT_DEPENDENCY_UNAVAILABLE", "The dependency is unavailable.", 503}
	ErrUnsupported = &Failure{"PASSPORT_FEATURE_UNSUPPORTED", "This option requires an unavailable provider.", 501}
)

func Normalize(e error) error {
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) || errors.As(e, &a) {
		return e
	}
	return ErrUnavailable
}

const (
	SingleChoice = iota
	MultipleChoice
	FreeText
)
const (
	Auto = iota
	Manual
)
const (
	InProgress = iota
	PendingReview
	Passed
	Failed
	Expired
)
const (
	Open = iota
	InProgressTicket
	Resolved
	Closed
)
const MaxQuestions = 200
const MaxChoices = 32

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Nick string `json:"nick"`
}
type Ticket struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Type          int        `json:"type"`
	Priority      int        `json:"priority"`
	Status        int        `json:"status"`
	CreatorID     string     `json:"creator_id"`
	Creator       Account    `json:"creator"`
	AssigneeID    *string    `json:"assignee_id"`
	Assignee      *Account   `json:"assignee"`
	Resources     []*string  `json:"resources"`
	Messages      []Message  `json:"messages"`
	MessagesTotal int        `json:"messages_total"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ResolvedAt    *time.Time `json:"resolved_at"`
	DeletedAt     *time.Time `json:"deleted_at"`
}
type Message struct {
	Replayed  bool      `json:"-"`
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	SenderID  string    `json:"sender_id"`
	Sender    Account   `json:"sender"`
	Content   string    `json:"content"`
	Files     []any     `json:"files"`
	CreatedAt time.Time `json:"created_at"`
}
type TicketCreate struct {
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Type      int       `json:"type"`
	Priority  int       `json:"priority"`
	FileIDs   []string  `json:"file_ids"`
	Resources []*string `json:"resources"`
}
type TicketUpdate struct {
	Title     *string    `json:"title"`
	Type      *int       `json:"type"`
	Priority  *int       `json:"priority"`
	Resources *[]*string `json:"resources"`
}
type MessageInput struct {
	Content string   `json:"content"`
	FileIDs []string `json:"file_ids"`
}
type OnCall struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	Account   Account   `json:"account"`
	CreatedAt time.Time `json:"created_at"`
}

type GroupInput struct {
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	Description *string        `json:"description"`
	Config      map[string]any `json:"config"`
}
type Group struct {
	ID string `json:"id"`
	GroupInput
	QuestionCount int        `json:"question_count"`
	Questions     []Question `json:"questions,omitempty"`
}
type ChoiceInput struct {
	ID        *string        `json:"id"`
	SortOrder int            `json:"sort_order"`
	Content   string         `json:"content"`
	IsCorrect bool           `json:"is_correct"`
	Config    map[string]any `json:"config"`
}
type QuestionInput struct {
	QuestionGroupKey string         `json:"question_group_key"`
	SortOrder        *int           `json:"sort_order"`
	Content          string         `json:"content"`
	Category         *string        `json:"category"`
	Type             int            `json:"type"`
	GradingMode      int            `json:"grading_mode"`
	Difficulty       int            `json:"difficulty"`
	Points           float64        `json:"points"`
	Config           map[string]any `json:"config"`
	Choices          []ChoiceInput  `json:"choices"`
}
type Choice struct {
	ID        string         `json:"id"`
	SortOrder int            `json:"sort_order"`
	Content   string         `json:"content"`
	IsCorrect bool           `json:"is_correct"`
	Config    map[string]any `json:"config"`
}
type Question struct {
	ID          string         `json:"id"`
	GroupID     string         `json:"question_group_id"`
	SortOrder   int            `json:"sort_order"`
	Content     string         `json:"content"`
	Category    *string        `json:"category"`
	Type        int            `json:"type"`
	GradingMode int            `json:"grading_mode"`
	Difficulty  int            `json:"difficulty"`
	Points      float64        `json:"points"`
	Config      map[string]any `json:"config"`
	Choices     []Choice       `json:"choices"`
}
type Assignment struct {
	QuestionGroupKey string `json:"question_group_key"`
	SortOrder        int    `json:"sort_order"`
}
type TestInput struct {
	Key                       string         `json:"key"`
	Title                     string         `json:"title"`
	Description               *string        `json:"description"`
	IsPublished               bool           `json:"is_published"`
	IsListed                  bool           `json:"is_listed"`
	ShuffleQuestions          bool           `json:"shuffle_questions"`
	AllowCategorySelection    bool           `json:"allow_category_selection"`
	RandomQuestionCount       *int           `json:"random_question_count"`
	SimpleQuestionPercentage  int            `json:"simple_question_percentage"`
	PassingScore              float64        `json:"passing_score"`
	MaxAttempts               *int           `json:"max_attempts"`
	AttemptPeriodDays         int            `json:"attempt_period_days"`
	TimeLimitSeconds          *int           `json:"time_limit_seconds"`
	RewardExperience          *int64         `json:"reward_experience"`
	GrantedPermissionGroupKey *string        `json:"granted_permission_group_key"`
	Config                    map[string]any `json:"config"`
	QuestionGroups            []Assignment   `json:"question_groups"`
}

func DefaultTestInput() TestInput {
	return TestInput{IsListed: true, SimpleQuestionPercentage: 60, PassingScore: 100, AttemptPeriodDays: 365}
}

type Test struct {
	ID string `json:"id"`
	TestInput
	IsArchived bool      `json:"is_archived"`
	Groups     []Group   `json:"groups"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type TrialInput struct {
	Key         string  `json:"key"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	IsPublished bool    `json:"is_published"`
	TestKey     string  `json:"test_key"`
}
type Trial struct {
	ID string `json:"id"`
	TrialInput
	TestTitle string `json:"test_title"`
}
type Snapshot struct {
	Key                       string     `json:"key"`
	Title                     string     `json:"title"`
	PassingScore              float64    `json:"passing_score"`
	RewardExperience          *int64     `json:"reward_experience"`
	GrantedPermissionGroupKey *string    `json:"granted_permission_group_key"`
	SelectedCategories        []string   `json:"selected_categories"`
	Questions                 []Question `json:"questions"`
}
type ParticipantChoice struct {
	ID      string         `json:"id"`
	Content string         `json:"content"`
	Config  map[string]any `json:"config"`
}
type ParticipantQuestion struct {
	ID         string              `json:"id"`
	Content    string              `json:"content"`
	Category   *string             `json:"category"`
	Type       int                 `json:"type"`
	Difficulty int                 `json:"difficulty"`
	Points     float64             `json:"points"`
	Config     map[string]any      `json:"config"`
	Choices    []ParticipantChoice `json:"choices"`
}
type ParticipantTest struct {
	Key                    string                `json:"key"`
	Title                  string                `json:"title"`
	Description            *string               `json:"description"`
	TimeLimitSeconds       *int                  `json:"time_limit_seconds"`
	RewardExperience       *int64                `json:"reward_experience"`
	MaxAttempts            *int                  `json:"max_attempts"`
	AllowCategorySelection bool                  `json:"allow_category_selection"`
	AvailableCategories    []string              `json:"available_categories"`
	Questions              []ParticipantQuestion `json:"questions"`
}
type AnswerInput struct {
	QuestionID string   `json:"question_id"`
	ChoiceIDs  []string `json:"choice_ids"`
	Text       *string  `json:"text"`
}
type Answer struct {
	ID            string      `json:"id"`
	QuestionID    string      `json:"question_id"`
	Value         AnswerValue `json:"value"`
	IsCorrect     *bool       `json:"is_correct"`
	AwardedPoints *float64    `json:"awarded_points"`
	ReviewNote    *string     `json:"review_note"`
	ReviewedAt    *time.Time  `json:"reviewed_at,omitempty"`
	ReviewedByID  *string     `json:"reviewed_by_id,omitempty"`
}
type AnswerValue struct {
	ChoiceIDs []string `json:"choice_ids"`
	Text      *string  `json:"text"`
}
type Attempt struct {
	ID                string                `json:"id"`
	Key               string                `json:"key"`
	Title             string                `json:"title"`
	AccountID         string                `json:"account_id,omitempty"`
	IsTrial           bool                  `json:"is_trial"`
	Status            int                   `json:"status"`
	StartedAt         time.Time             `json:"started_at"`
	DeadlineAt        *time.Time            `json:"deadline_at"`
	SubmittedAt       *time.Time            `json:"submitted_at"`
	ReviewedAt        *time.Time            `json:"reviewed_at"`
	Score             *float64              `json:"score"`
	Questions         []ParticipantQuestion `json:"questions"`
	Answers           []Answer              `json:"answers"`
	PermissionClaim   string                `json:"permission_claim"`
	RewardPending     bool                  `json:"reward_pending"`
	ActivationPending bool                  `json:"activation_pending"`
	Snapshot          *Snapshot             `json:"snapshot,omitempty"`
}
type ReviewInput struct {
	IsCorrect     bool    `json:"is_correct"`
	AwardedPoints float64 `json:"awarded_points"`
	Note          *string `json:"note"`
}
type ActivationTest struct {
	Key              string `json:"key"`
	Title            string `json:"title"`
	Available        bool   `json:"available"`
	Passed           bool   `json:"passed"`
	MaxAttempts      *int   `json:"max_attempts"`
	UsedAttemptCount int    `json:"used_attempt_count"`
}
type ActivationState struct {
	IsActivated               bool             `json:"is_activated"`
	ActivationPending         bool             `json:"activation_pending"`
	PolicyConfigured          bool             `json:"policy_configured"`
	TestsEnabled              bool             `json:"tests_enabled"`
	TestsBypassed             bool             `json:"tests_bypassed"`
	RequireVerifiedContact    bool             `json:"require_verified_contact"`
	HasVerifiedContact        bool             `json:"has_verified_contact"`
	Tests                     []ActivationTest `json:"tests"`
	IsSatisfied               bool             `json:"is_satisfied"`
	RequiredRequirementCount  int              `json:"required_requirement_count"`
	CompletedRequirementCount int              `json:"completed_requirement_count"`
}

type Query struct {
	Offset, Take                    int
	CreatorID, AssigneeID, GroupKey string
	Type, Status, Priority          *int
}
type Command struct {
	Operation, Key, ID, IdempotencyKey string
	Query                              Query
	TicketCreate                       *TicketCreate
	TicketUpdate                       *TicketUpdate
	Message                            *MessageInput
	Group                              *GroupInput
	Question                           *QuestionInput
	Questions                          []QuestionInput
	Test                               *TestInput
	Trial                              *TrialInput
	Categories                         []string
	Answers                            []AnswerInput
	Review                             *ReviewInput
	AccountID                          *string
	State                              *int
	Toggle                             *bool
}
type Result struct {
	Value       any
	Total       *int64
	Changed     bool
	ResourceID  string
	AfterCommit error
}
type Repository interface {
	Execute(context.Context, string, Command) (Result, error)
}

// UnmarshalJSON preserves the authored one-point default in both individual
// question creation and nested bulk imports, while rejecting unknown fields.
func (q *QuestionInput) UnmarshalJSON(data []byte) error {
	type wire QuestionInput
	value := wire{Points: 1}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&value); e != nil {
		return e
	}
	*q = QuestionInput(value)
	return nil
}
