// SPDX-License-Identifier: AGPL-3.0-only
package fortunes

import (
	"context"
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
	ErrInvalid     = &Failure{"FORTUNE_INVALID_REQUEST", "Invalid fortune request.", 400}
	ErrNotFound    = &Failure{"PASSPORT_FORTUNE_NOT_FOUND", "No fortunes found for the specified language.", 404}
	ErrUnavailable = &Failure{"FORTUNE_UNAVAILABLE", "A fortune provider or corpus is unavailable.", 503}
	ErrConflict    = &Failure{"FORTUNE_CONFLICT", "Fortune configuration conflicts with existing state.", 409}
	ErrProvider    = &Failure{"FORTUNE_PROVIDER_FAILED", "Fortune generation is pending.", 503}
)

func Normalize(e error) error {
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) || errors.As(e, &a) {
		return e
	}
	return ErrUnavailable
}

type Saying struct {
	Content  string `json:"content"`
	Source   string `json:"source"`
	Language string `json:"language"`
}
type Tip struct {
	IsPositive bool   `json:"is_positive"`
	Title      string `json:"title"`
	Content    string `json:"content"`
}
type Report struct {
	Version        int    `json:"version"`
	Poem           string `json:"poem"`
	Summary        string `json:"summary"`
	SummaryDetail  string `json:"summary_detail"`
	Wish           string `json:"wish"`
	Love           string `json:"love"`
	Study          string `json:"study"`
	Career         string `json:"career"`
	Health         string `json:"health"`
	LostItem       string `json:"lost_item"`
	LuckyColor     string `json:"lucky_color"`
	LuckyDirection string `json:"lucky_direction"`
	LuckyTime      string `json:"lucky_time"`
	LuckyItem      string `json:"lucky_item"`
	LuckyAction    string `json:"lucky_action"`
	AvoidAction    string `json:"avoid_action"`
	Ritual         string `json:"ritual"`
}
type Generation struct {
	Report Report `json:"report"`
	Tips   []Tip  `json:"tips"`
}

// Request contains only the canonical check-in and language/time-zone context.
// The provider transport is an explicit owned contract, not the original RPC.
type Request struct {
	Version        int    `json:"version"`
	IdempotencyKey string `json:"idempotency_key"`
	AccountID      string `json:"account_id"`
	CheckInID      string `json:"check_in_id"`
	Day            string `json:"day"`
	LocalDate      string `json:"local_date"`
	Language       string `json:"language"`
	TimeZone       string `json:"time_zone"`
	Level          int    `json:"level"`
}
type Provider interface {
	Revision() string
	Generate(context.Context, Request) (Generation, error)
}
type Template struct {
	ID         string     `json:"id"`
	Language   string     `json:"language"`
	Level      int        `json:"level"`
	Generation Generation `json:"generation"`
}
type Corpus struct {
	Revision   string     `json:"revision"`
	Label      string     `json:"label"`
	Provenance string     `json:"provenance"`
	Sayings    []Saying   `json:"sayings"`
	Reports    []Template `json:"reports"`
}
type Status struct {
	State     string    `json:"state"`
	Provider  string    `json:"provider,omitempty"`
	Attempts  int       `json:"attempts"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Projection struct {
	Generation *Generation
	Status     Status
}
type Repository interface {
	Catalogue(context.Context, string, string, int, int, bool) ([]Saying, int64, error)
}
