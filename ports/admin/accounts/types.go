// SPDX-License-Identifier: AGPL-3.0-only
// Source-backed Passport account administration; see the continuation document.
package accounts

import (
	"context"
	"encoding/json"
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"time"
	"unicode/utf8"
)

const ViewPermission = "accounts.view"
const BadgePermission = "progression.badges.manage"

var ErrAccountNotFound = &c.Failure{Code: "PASSPORT_ACCOUNT_NOT_FOUND", Message: "Account not found.", Status: http.StatusNotFound}

type ActivityMetrics struct {
	CalculatedAt               time.Time `json:"calculated_at"`
	CurrentDayStartedAt        time.Time `json:"current_day_started_at"`
	DailyActiveUsers           int64     `json:"daily_active_users"`
	WeeklyActiveUsers          int64     `json:"weekly_active_users"`
	MonthlyActiveUsers         int64     `json:"monthly_active_users"`
	PreviousDailyActiveUsers   int64     `json:"previous_daily_active_users"`
	PreviousWeeklyActiveUsers  int64     `json:"previous_weekly_active_users"`
	PreviousMonthlyActiveUsers int64     `json:"previous_monthly_active_users"`
	NewAccountsToday           int64     `json:"new_accounts_today"`
	NewAccountsThisWeek        int64     `json:"new_accounts_this_week"`
	NewAccountsThisMonth       int64     `json:"new_accounts_this_month"`
	TotalProfiledAccounts      int64     `json:"total_profiled_accounts"`
}

type Repository interface {
	AccountActivityMetrics(context.Context, string) (ActivityMetrics, error)
	AccountContacts(context.Context, string, string) ([]signin.Contact, error)
	AccountFactors(context.Context, string, string) ([]signin.FactorSummary, error)
	AccountBadges(context.Context, string, string) ([]progression.Badge, error)
	GrantAccountBadge(context.Context, string, string, progression.AdminBadge) (progression.Badge, error)
	ActivateAccountBadge(context.Context, string, string, string) error
	RevokeAccountBadge(context.Context, string, string, string) error
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}

// Arbitrary administrative badge types are display data, never roles or grants.
func ValidateBadge(v progression.AdminBadge) error {
	if strings.TrimSpace(v.Type) != v.Type || v.Type == "" || !utf8.ValidString(v.Type) || strings.ContainsRune(v.Type, 0) || utf8.RuneCountInString(v.Type) > 1024 {
		return c.ErrInvalid
	}
	for _, field := range []struct {
		value *string
		limit int
	}{{v.Label, 1024}, {v.Caption, 4096}} {
		if s := field.value; s != nil && (!utf8.ValidString(*s) || strings.ContainsRune(*s, 0) || utf8.RuneCountInString(*s) > field.limit) {
			return c.ErrInvalid
		}
	}
	b, e := json.Marshal(v.Meta)
	if e != nil || len(b) > 16384 {
		return c.ErrInvalid
	}
	return nil
}
