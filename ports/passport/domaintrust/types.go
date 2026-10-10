// SPDX-License-Identifier: AGPL-3.0-only
package domaintrust

import (
	"context"
	"net/http"
	"time"
)

const (
	Blocked  = 0
	Neutral  = 1
	Verified = 2
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"DOMAIN_TRUST_INVALID", "Invalid domain trust request.", 400}
	ErrNotFound    = &Failure{"DOMAIN_TRUST_NOT_FOUND", "Domain trust record not found.", 404}
	ErrForbidden   = &Failure{"DOMAIN_TRUST_FORBIDDEN", "Global administration is required.", 403}
	ErrUnavailable = &Failure{"DOMAIN_TRUST_UNAVAILABLE", "Domain trust is unavailable.", 503}
	ErrCapacity    = &Failure{"DOMAIN_TRUST_CAPACITY", "Domain trust storage capacity reached.", 503}
)

type Rule struct {
	ID                 string     `json:"id"`
	DomainPattern      string     `json:"domain_pattern"`
	Protocol           *string    `json:"protocol"`
	PortRestriction    *int       `json:"port_restriction"`
	Reason             *string    `json:"reason"`
	Priority           int        `json:"priority"`
	TrustLevel         int        `json:"trust_level"`
	IsActive           bool       `json:"is_active"`
	CreatedByAccountID *string    `json:"created_by_account_id"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	DeletedAt          *time.Time `json:"deleted_at"`
}
type Create struct {
	DomainPattern   string  `json:"domain_pattern"`
	Protocol        *string `json:"protocol"`
	PortRestriction *int    `json:"port_restriction"`
	Reason          *string `json:"reason"`
	Priority        int     `json:"priority"`
	TrustLevel      int     `json:"trust_level"`
	IsActive        *bool   `json:"is_active"`
}
type Patch struct {
	DomainPattern   *string `json:"domain_pattern"`
	Protocol        *string `json:"protocol"`
	PortRestriction *int    `json:"port_restriction"`
	Reason          *string `json:"reason"`
	Priority        *int    `json:"priority"`
	TrustLevel      *int    `json:"trust_level"`
	IsActive        *bool   `json:"is_active"`
}
type Result struct {
	IsAllowed     bool    `json:"is_allowed"`
	IsVerified    bool    `json:"is_verified"`
	BlockReason   *string `json:"block_reason"`
	MatchedRule   *Rule   `json:"matched_rule"`
	MatchedSource *string `json:"matched_source"`
}
type Metric struct {
	Domain        string    `json:"domain"`
	CheckCount    int64     `json:"check_count"`
	BlockedCount  int64     `json:"blocked_count"`
	VerifiedCount int64     `json:"verified_count"`
	LastCheckedAt time.Time `json:"last_checked_at"`
}
type MetricMaintenance struct {
	OlderThan time.Time `json:"older_than"`
	Limit     int       `json:"limit"`
}
type MetricPurgeResult struct {
	Purged int64 `json:"purged"`
}
type Page struct{ Offset, Limit int }

func (p Page) Bounds() Page {
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Offset > 100000 {
		p.Offset = 100000
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	if p.Limit > 200 {
		p.Limit = 200
	}
	return p
}

type Evaluator interface {
	Evaluate(context.Context, string) (Result, error)
}
type Repository interface {
	Evaluator
	Validate(context.Context, string) (Result, error)
	List(context.Context, string, Page) ([]Rule, int, error)
	Get(context.Context, string, string) (Rule, error)
	Create(context.Context, string, Create) (Rule, error)
	Update(context.Context, string, string, Patch) (Rule, error)
	Delete(context.Context, string, string) error
	Metrics(context.Context, string, Page) ([]Metric, int, error)
	Metric(context.Context, string, string) (Metric, error)
	PurgeMetrics(context.Context, string, MetricMaintenance) (MetricPurgeResult, error)
}
type Auth func(*http.Request, bool) (string, error)
