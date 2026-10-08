// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"CALENDAR_INVALID_REQUEST", "Invalid calendar request.", 400}
	ErrNotFound    = &Failure{"CALENDAR_NOT_FOUND", "Calendar item not found.", 404}
	ErrForbidden   = &Failure{"FORBIDDEN", "Permission denied.", 403}
	ErrConflict    = &Failure{"CALENDAR_CONFLICT", "The request conflicts with existing state.", 409}
	ErrLimit       = &Failure{"CALENDAR_RESULT_LIMIT", "Narrow the requested calendar range.", 400}
	ErrUnsupported = &Failure{"CALENDAR_FEATURE_UNSUPPORTED", "The requested calendar option is unsupported.", 501}
	ErrUnavailable = &Failure{"CALENDAR_UNAVAILABLE", "A calendar dependency is unavailable.", 503}
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
	Private = 0
	Friends = 100
	Public  = 200
)
const (
	None = iota
	Daily
	Weekly
	Monthly
	Yearly
)
const (
	Holiday = iota
	EventTag
	Anniversary
	Memorial
	Festival
)
const (
	MaxResults    = 4096
	MaxCandidates = 1000
)

type Recurrence struct {
	Frequency   int        `json:"frequency"`
	Interval    int        `json:"interval"`
	DaysOfWeek  []string   `json:"days_of_week,omitempty"`
	DayOfMonth  *int       `json:"day_of_month,omitempty"`
	MonthOfYear *int       `json:"month_of_year,omitempty"`
	EndDate     *time.Time `json:"end_date,omitempty"`
	Occurrences *int       `json:"occurrences,omitempty"`
}

func (r *Recurrence) UnmarshalJSON(b []byte) error {
	type wire Recurrence
	v := wire{Interval: 1}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(&v); e != nil {
		return e
	}
	*r = Recurrence(v)
	return nil
}

type EventBody struct {
	Title       string          `json:"title"`
	Description *string         `json:"description"`
	Location    *string         `json:"location"`
	StartTime   time.Time       `json:"start_time"`
	EndTime     time.Time       `json:"end_time"`
	IsAllDay    bool            `json:"is_all_day"`
	Visibility  int             `json:"visibility"`
	Recurrence  *Recurrence     `json:"recurrence"`
	Tags        []string        `json:"tags"`
	Meta        json.RawMessage `json:"meta"`
	TimeZone    string          `json:"time_zone"`
}
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Nick string `json:"nick"`
}
type Event struct {
	ID string `json:"id"`
	EventBody
	AccountID     string     `json:"account_id"`
	Account       Account    `json:"account"`
	Icon          any        `json:"icon"`
	Background    any        `json:"background"`
	OccurrenceKey string     `json:"occurrence_key,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at"`
	Version       int64      `json:"version"`
}
type EventPatch struct {
	Title             *string         `json:"title"`
	Description       *string         `json:"description"`
	Location          *string         `json:"location"`
	StartTime         *time.Time      `json:"start_time"`
	EndTime           *time.Time      `json:"end_time"`
	IsAllDay          *bool           `json:"is_all_day"`
	Visibility        *int            `json:"visibility"`
	Recurrence        *Recurrence     `json:"recurrence"`
	Tags              *[]string       `json:"tags"`
	Meta              json.RawMessage `json:"meta"`
	TimeZone          *string         `json:"time_zone"`
	IconID            *string         `json:"icon_id"`
	BackgroundID      *string         `json:"background_id"`
	RecurrencePresent bool            `json:"-"`
}
type DayInput struct {
	Name              string          `json:"name"`
	Description       *string         `json:"description"`
	LocalName         *string         `json:"local_name"`
	LocalizableKey    *string         `json:"localizable_key"`
	StartDate         time.Time       `json:"start_date"`
	EndDate           time.Time       `json:"end_date"`
	IsAllDay          bool            `json:"is_all_day"`
	Region            string          `json:"region"`
	Tags              []int           `json:"tags"`
	Meta              json.RawMessage `json:"meta"`
	IsRecurring       bool            `json:"is_recurring"`
	RecurrencePattern *string         `json:"recurrence_pattern"`
	IsPeriod          bool            `json:"is_period"`
	HolidayDays       []string        `json:"holiday_days"`
	DisplayOrder      *int            `json:"display_order"`
}
type Day struct {
	ID string `json:"id"`
	DayInput
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}
type Occurrence struct {
	Date           time.Time       `json:"date"`
	LocalName      string          `json:"local_name"`
	GlobalName     string          `json:"global_name"`
	LocalizableKey *string         `json:"localizable_key"`
	CountryCode    *string         `json:"country_code"`
	Description    *string         `json:"description"`
	Meta           json.RawMessage `json:"meta"`
	Holidays       []string        `json:"holidays"`
	Tags           []int           `json:"tags"`
	OccurrenceKey  string          `json:"occurrence_key"`
	SourceID       *string         `json:"source_id,omitempty"`
}
type Subscription struct {
	ID              string    `json:"id"`
	SubscriberID    string    `json:"subscriber_id"`
	TargetAccountID string    `json:"target_account_id"`
	CreatedAt       time.Time `json:"created_at"`
}
type SearchItem struct {
	Type       int         `json:"type"`
	StartTime  time.Time   `json:"start_time"`
	EndTime    time.Time   `json:"end_time"`
	UserEvent  *Event      `json:"user_event,omitempty"`
	NotableDay *Occurrence `json:"notable_day,omitempty"`
}
type Countdown struct {
	EventID        *string         `json:"event_id"`
	OccurrenceKey  string          `json:"occurrence_key,omitempty"`
	EventType      int             `json:"event_type"`
	Title          string          `json:"title"`
	Description    *string         `json:"description"`
	Location       *string         `json:"location"`
	StartTime      time.Time       `json:"start_time"`
	EndTime        time.Time       `json:"end_time"`
	IsAllDay       bool            `json:"is_all_day"`
	DaysRemaining  int64           `json:"days_remaining"`
	HoursRemaining int64           `json:"hours_remaining"`
	IsOngoing      bool            `json:"is_ongoing"`
	Meta           json.RawMessage `json:"meta"`
	AccountID      *string         `json:"account_id"`
	Icon           any             `json:"icon"`
	Background     any             `json:"background"`
}
type Overlay struct {
	Date     string
	CheckIn  json.RawMessage
	Statuses []json.RawMessage
	Merged   []MergedEvent
}
type DailyCalendar struct {
	Date                  time.Time         `json:"date"`
	CheckInResult         json.RawMessage   `json:"check_in_result"`
	Statuses              []json.RawMessage `json:"statuses"`
	UserEvents            []Event           `json:"user_events"`
	NotableDays           []Occurrence      `json:"notable_days"`
	UnavailableComponents []string          `json:"unavailable_components,omitempty"`
}
type MergedEvent struct {
	ID            *string         `json:"id"`
	Type          int             `json:"type"`
	Title         string          `json:"title"`
	Description   *string         `json:"description"`
	Location      *string         `json:"location"`
	StartTime     time.Time       `json:"start_time"`
	EndTime       time.Time       `json:"end_time"`
	IsAllDay      bool            `json:"is_all_day"`
	Meta          json.RawMessage `json:"meta"`
	OccurrenceKey string          `json:"occurrence_key,omitempty"`
}
type MergedCalendar struct {
	Date                  time.Time         `json:"date"`
	CheckInResult         json.RawMessage   `json:"check_in_result"`
	Statuses              []json.RawMessage `json:"statuses"`
	UserEvents            []Event           `json:"user_events"`
	NotableDays           []Occurrence      `json:"notable_days"`
	MergedEvents          []MergedEvent     `json:"merged_events"`
	UnavailableComponents []string          `json:"unavailable_components,omitempty"`
}
type Query struct {
	Year, Month, Offset, Take                int
	Region, Owner, AccountID, Text, TimeZone string
	Tags                                     []string
	Tag, Layer                               *int
	StartTime, EndTime                       *time.Time
	IncludeNotable                           bool
}
type Command struct {
	Operation, ID, IdempotencyKey, OccurrenceKey, TargetID string
	Query                                                  Query
	Day                                                    *DayInput
	Patch                                                  *EventPatch
}
type Result struct {
	Value      any
	Total      *int64
	Changed    bool
	ResourceID string
}
type Repository interface {
	Execute(context.Context, string, Command) (Result, error)
}
