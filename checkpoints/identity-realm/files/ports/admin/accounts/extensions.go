// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"time"
	"unicode/utf8"
)

const ManagePermission = "accounts.manage"

type Filter struct {
	Query        string
	Offset, Take int
	OrderBy      string
}
type Summary struct {
	Account             accounts.Account `json:"account"`
	Status              presence.Status  `json:"status"`
	BadgeCount          int              `json:"badge_count"`
	ActiveActivityCount int              `json:"active_activity_count"`
}
type Detail struct {
	Summary
	Activities      []activities.Activity  `json:"activities"`
	Contacts        []signin.Contact       `json:"contacts"`
	AuthFactors     []signin.FactorSummary `json:"auth_factors"`
	Badges          []progression.Badge    `json:"badges"`
	Board           []map[string]any       `json:"board"`
	BoardConfigured bool                   `json:"board_configured"`
}

// Session is a security projection; refresh hashes, sealed pairs and opaque
// proofs never cross the repository boundary.
type Session struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Type      int    `json:"type"`
	ParentID  string `json:"parent_id,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	CreatedAt any    `json:"created_at"`
	ExpiresAt any    `json:"expires_at"`
	RevokedAt any    `json:"revoked_at"`
}
type CreditInput struct {
	Record     string     `json:"record"`
	ReasonType string     `json:"reason_type"`
	Reason     string     `json:"reason"`
	Delta      float64    `json:"delta"`
	ExpiresAt  *time.Time `json:"expired_at"`
}
type Mutation struct {
	Credit     *CreditInput         `json:"credit,omitempty"`
	Operation  string               `json:"-"`
	ResourceID string               `json:"-"`
	Contact    *signin.ContactInput `json:"contact,omitempty"`
	Public     *bool                `json:"is_public,omitempty"`
	Password   *string              `json:"password,omitempty"`
}
type ExtendedRepository interface {
	ListAccounts(context.Context, string, Filter) ([]Summary, int, error)
	AccountDetail(context.Context, string, string) (Detail, error)
	AccountSessions(context.Context, string, string) ([]Session, error)
	MutateAccount(context.Context, string, string, Mutation) (any, error)
}

func ValidateFilter(f Filter) error {
	if f.Take < 1 || f.Take > 200 || f.Offset < 0 || f.Offset > 100000 || len(f.Query) > 256 || !utf8.ValidString(f.Query) || strings.ContainsRune(f.Query, 0) {
		return authority.ErrInvalid
	}
	switch f.OrderBy {
	case "", "created_at_desc", "created_at_asc", "name_asc", "name_desc":
		return nil
	}
	return authority.ErrInvalid
}

func extensionPath(p []string) bool {
	if len(p) == 3 || len(p) == 4 {
		return true
	}
	if len(p) == 5 {
		return p[4] == "credentials" || p[4] == "sessions" || p[4] == "credits"
	}
	if len(p) == 6 {
		if p[5] == "" {
			return false
		}
		return p[4] == "contacts" || p[4] == "factors" || p[4] == "sessions"
	}
	return len(p) == 7 && p[4] == "contacts" && (p[6] == "verify" || p[6] == "primary")
}

// Keep the original badge/read repository contract stable for narrow adapters.
func extensionRepository(repo Repository) (ExtendedRepository, error) {
	r, ok := repo.(ExtendedRepository)
	if !ok {
		return nil, authority.ErrUnavailable
	}
	return r, nil
}
