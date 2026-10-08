// SPDX-License-Identifier: AGPL-3.0-only
package affiliation

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	"unicode/utf8"
)

// Owned JSON mappings. The excluded Shared enum was not inspected.
const (
	Tracking           = 0
	RegistrationInvite = 1
)

var (
	ErrInvalid     = &authority.Failure{Code: "PASSPORT_AFFILIATION_INVALID", Message: "Invalid affiliation request.", Status: 400}
	ErrInvite      = &authority.Failure{Code: "PASSPORT_AFFILIATION_INVITE_INVALID", Message: "The invitation is invalid, expired, revoked, or exhausted.", Status: 400}
	ErrRestricted  = &authority.Failure{Code: "PASSPORT_AFFILIATION_INVITE_USE_RESTRICTED", Message: "Registration invitations require first-party account admission.", Status: 400}
	ErrConflict    = &authority.Failure{Code: "PASSPORT_AFFILIATION_CONFLICT", Message: "The affiliation already exists or conflicts with prior admission.", Status: 409}
	ErrNotFound    = &authority.Failure{Code: "PASSPORT_AFFILIATION_SPELL_NOT_FOUND", Message: "Affiliation spell not found.", Status: 404}
	ErrUnavailable = &authority.Failure{Code: "AFFILIATION_UNAVAILABLE", Message: "Affiliation service is unavailable.", Status: 503}
	ErrPurchase    = &authority.Failure{Code: "AFFILIATION_PURCHASE_UNCONFIGURED", Message: "Registration invitation purchases are not configured.", Status: 503}
)

type Meta struct {
	MaxUsages *int `json:"max_usages"`
	SkipTests bool `json:"skip_tests"`
}

// InviteIssuePermission is an owned administrative capability, never inferred
// from ordinary tracking-code management or granted by activation.
const InviteIssuePermission = "affiliations.invites.issue"

type Spell struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	Spell      string     `json:"spell"`
	Type       int        `json:"type"`
	Meta       Meta       `json:"meta"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	AffectedAt *time.Time `json:"affected_at"`
	UsageCount int64      `json:"usage_count"`
}
type Event struct {
	ID                 string    `json:"id"`
	SpellID            string    `json:"spell_id"`
	ResourceIdentifier string    `json:"resource_identifier"`
	CreatedAt          time.Time `json:"created_at"`
}
type Create struct {
	Spell     *string `json:"spell"`
	MaxUsages *int    `json:"max_usages"`
	SkipTests *bool   `json:"skip_tests"`
}

// InviteCreate is an explicit owned admin extension; the original tracking
// create request and route do not acquire a type field or new semantics.
type InviteCreate struct {
	Create
	ExpiresAt *time.Time `json:"expires_at"`
}
type Query struct {
	Take, Offset int
	Order        string
	Desc         bool
}
type Command struct {
	Operation, ID, Word, Resource string
	Create                        *Create
	ExpiresAt                     *time.Time
	Query                         Query
}
type Result struct {
	Value any
	Total *int64
}
type Repository interface {
	Execute(context.Context, string, Command) (Result, error)
	BindAuthority(context.Context, authority.Principal) context.Context
}

func ValidWord(v string) bool {
	return utf8.ValidString(v) && len(v) > 0 && utf8.RuneCountInString(v) <= 1024 && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "/\\\x00\r\n")
}
func ValidResource(v string) bool {
	return utf8.ValidString(v) && strings.TrimSpace(v) != "" && utf8.RuneCountInString(v) <= 8192 && !strings.ContainsRune(v, 0)
}

// Root rejects delegated/app/OAuth sessions even when they have a permission.
// The caller must supply a freshly verified canonical principal.
func Root(p authority.Principal) error {
	if !authority.ValidID(p.Account.ID) || !authority.ValidID(p.Session.ID) || p.Session.AccountID != p.Account.ID || p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "" {
		return authority.ErrForbidden
	}
	return nil
}
