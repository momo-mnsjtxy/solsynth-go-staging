// SPDX-License-Identifier: AGPL-3.0-only
package progression

import (
	"context"
	"encoding/json"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"VALIDATION_ERROR", "Invalid progression request.", 400}
	ErrNotFound    = &Failure{"NOT_FOUND", "The requested progression record does not exist.", 404}
	ErrForbidden   = &Failure{"FORBIDDEN", "Permission denied.", 403}
	ErrConflict    = &Failure{"CONFLICT", "The request conflicts with an existing record.", 409}
	ErrUnavailable = &Failure{"PROGRESSION_UNAVAILABLE", "Progression is unavailable.", 503}
	ErrUnsupported = &Failure{"DEPENDENCY_UNSUPPORTED", "This reward or check-in option requires an unconfigured provider.", 501}
)

// Nested trigger/schedule encodings are owned strings; no excluded enum is imported.
type Trigger struct {
	Actions    []string          `json:"actions"`
	Mode       string            `json:"mode"`
	MetaEquals map[string]string `json:"meta_equals"`
}
type BadgeReward struct {
	Type    string         `json:"type"`
	Label   *string        `json:"label"`
	Caption *string        `json:"caption"`
	Meta    map[string]any `json:"meta"`
}
type Reward struct {
	Experience           int64        `json:"experience"`
	SourcePoints         int64        `json:"source_points"`
	SourcePointsCurrency *string      `json:"source_points_currency"`
	Badge                *BadgeReward `json:"badge"`
}
type Schedule struct {
	Repeatability string `json:"repeatability"`
}
type Definition struct {
	Identifier        string     `json:"identifier"`
	Title             string     `json:"title"`
	Summary           string     `json:"summary"`
	Icon              *string    `json:"icon"`
	SeriesIdentifier  *string    `json:"series_identifier"`
	SeriesTitle       *string    `json:"series_title"`
	SeriesOrder       *int       `json:"series_order"`
	SortOrder         int        `json:"sort_order"`
	Hidden            bool       `json:"hidden"`
	IsEnabled         bool       `json:"is_enabled"`
	IsSeedManaged     bool       `json:"is_seed_managed"`
	IsProgressEnabled bool       `json:"is_progress_enabled"`
	AvailableFrom     *time.Time `json:"available_from"`
	AvailableUntil    *time.Time `json:"available_until"`
	TargetCount       int        `json:"target_count"`
	Trigger           Trigger    `json:"trigger"`
	Reward            Reward     `json:"reward"`
	Schedule          *Schedule  `json:"schedule,omitempty"`
}
type SeriesStage struct {
	Identifier  string     `json:"identifier"`
	Title       string     `json:"title"`
	SeriesOrder *int       `json:"series_order"`
	TargetCount int        `json:"target_count"`
	IsCompleted bool       `json:"is_completed"`
	CompletedAt *time.Time `json:"completed_at"`
}
type State struct {
	Identifier           string        `json:"identifier"`
	Title                string        `json:"title"`
	Summary              string        `json:"summary"`
	Icon                 *string       `json:"icon"`
	SeriesIdentifier     *string       `json:"series_identifier"`
	SeriesTitle          *string       `json:"series_title"`
	SeriesOrder          *int          `json:"series_order"`
	SortOrder            int           `json:"sort_order"`
	Hidden               bool          `json:"hidden"`
	IsEnabled            bool          `json:"is_enabled"`
	IsProgressEnabled    bool          `json:"is_progress_enabled"`
	IsCurrentlyAvailable bool          `json:"is_currently_available"`
	AvailableFrom        *time.Time    `json:"available_from"`
	AvailableUntil       *time.Time    `json:"available_until"`
	TargetCount          int           `json:"target_count"`
	ProgressCount        int           `json:"progress_count"`
	CurrentStreak        *int          `json:"current_streak,omitempty"`
	BestStreak           *int          `json:"best_streak,omitempty"`
	SeriesTotalSteps     int           `json:"series_total_steps"`
	SeriesCompletedSteps int           `json:"series_completed_steps"`
	SeriesStages         []SeriesStage `json:"series_stages"`
	IsCompleted          bool          `json:"is_completed"`
	CompletedAt          *time.Time    `json:"completed_at"`
	Reward               Reward        `json:"reward"`
	PeriodKey            *string       `json:"period_key,omitempty"`
	NextResetAt          *time.Time    `json:"next_reset_at,omitempty"`
	Schedule             *Schedule     `json:"schedule,omitempty"`
}
type Stats struct {
	TotalCount           int     `json:"total_count"`
	CompletedCount       int     `json:"completed_count"`
	HiddenTotalCount     int     `json:"hidden_total_count"`
	HiddenCompletedCount int     `json:"hidden_completed_count"`
	CompletionPercentage float64 `json:"completion_percentage"`
}
type Grant struct {
	ID                    string     `json:"id"`
	AccountID             string     `json:"account_id"`
	DefinitionType        string     `json:"definition_type"`
	DefinitionIdentifier  string     `json:"definition_identifier"`
	DefinitionTitle       string     `json:"definition_title"`
	RewardToken           string     `json:"reward_token"`
	SourceEventID         string     `json:"source_event_id"`
	PeriodKey             string     `json:"period_key"`
	Reward                Reward     `json:"reward"`
	BadgeGrantedAt        *time.Time `json:"badge_granted_at"`
	ExperienceGrantedAt   *time.Time `json:"experience_granted_at"`
	SourcePointsGrantedAt *time.Time `json:"source_points_granted_at"`
	NotificationSentAt    *time.Time `json:"notification_sent_at"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	DeletedAt             *time.Time `json:"deleted_at"`
}
type Completion struct {
	Kind       string `json:"kind"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	PeriodKey  string `json:"period_key"`
	Reward     Reward `json:"reward"`
}
type ExperienceRecord struct {
	ID              string     `json:"id"`
	AccountID       string     `json:"account_id"`
	ReasonType      string     `json:"reason_type"`
	Reason          string     `json:"reason"`
	Delta           int64      `json:"delta"`
	BonusMultiplier float64    `json:"bonus_multiplier"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at"`
}
type Badge struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Label       *string         `json:"label"`
	Caption     *string         `json:"caption"`
	Meta        json.RawMessage `json:"meta"`
	ExpiredAt   *time.Time      `json:"expired_at"`
	AccountID   string          `json:"account_id"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	ActivatedAt *time.Time      `json:"activated_at"`
	DeletedAt   *time.Time      `json:"deleted_at"`
}
type AdminBadge struct {
	Type        string         `json:"type"`
	Label       *string        `json:"label"`
	Caption     *string        `json:"caption"`
	Meta        map[string]any `json:"meta"`
	ActivatedAt *time.Time     `json:"activated_at"`
	ExpiredAt   *time.Time     `json:"expired_at"`
}
type CheckIn struct {
	ID               string     `json:"id"`
	AccountID        string     `json:"account_id"`
	Level            int        `json:"level"`
	Tips             []any      `json:"tips"`
	FortuneReport    any        `json:"fortune_report"`
	FortuneStatus    any        `json:"fortune_status,omitempty"`
	RewardExperience int64      `json:"reward_experience"`
	RewardPoints     *int64     `json:"reward_points"`
	BackdatedFrom    *time.Time `json:"backdated_from"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DeletedAt        *time.Time `json:"deleted_at"`
}

// Summary is an owned extension. A null level means no operator level policy is installed.
type Summary struct {
	Experience            int64    `json:"experience"`
	Level                 *int     `json:"level"`
	LevelingProgress      *float64 `json:"leveling_progress"`
	LevelPolicyConfigured bool     `json:"level_policy_configured"`
	ActiveBadge           *Badge   `json:"active_badge"`
}
type Repository interface {
	States(context.Context, string, string, string) ([]State, error)
	AchievementStats(context.Context, string) (Stats, error)
	Grants(context.Context, string, int) ([]Grant, error)
	Experience(context.Context, string, int, int) ([]ExperienceRecord, int64, error)
	Badges(context.Context, string, string) ([]Badge, error)
	ActivateBadge(context.Context, string, string) error
	CheckIn(context.Context, string) (CheckIn, error)
	Today(context.Context, string) (CheckIn, error)
	CheckInHistory(context.Context, string, int, int) ([]CheckIn, int64, error)
	Summary(context.Context, string) (Summary, error)
	Definitions(context.Context, string, string) ([]Definition, error)
	SaveDefinition(context.Context, string, string, string, Definition, bool) (Definition, error)
	EnableDefinition(context.Context, string, string, string, bool) error
	SetLevelPolicy(context.Context, string, []int64) error
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}

func ValidPage(offset, take int) bool {
	return offset >= 0 && offset <= 100000 && take >= 1 && take <= 100
}
