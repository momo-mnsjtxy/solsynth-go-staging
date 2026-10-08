// SPDX-License-Identifier: AGPL-3.0-only
package presence

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
	ErrInvalid     = &Failure{"VALIDATION_ERROR", "Invalid presence request.", 400}
	ErrNotFound    = &Failure{"PASSPORT_STATUS_NOT_FOUND", "Status not found.", 404}
	ErrForbidden   = &Failure{"FORBIDDEN", "Permission denied.", 403}
	ErrUnavailable = &Failure{"PRESENCE_UNAVAILABLE", "Presence is unavailable.", 503}
	ErrUnsupported = &Failure{"DEPENDENCY_UNSUPPORTED", "The requested media reference provider is not configured.", 501}
	ErrAutomated   = &Failure{"PASSPORT_STATUS_AUTOMATED_NO_UPDATE", "Automated status cannot be updated.", 400}
)

// Owned numeric encoding. It is explicit, not inferred from excluded Shared enums.
const (
	Default      = 0
	Busy         = 1
	DoNotDisturb = 2
	Invisible    = 3
)

type StatusRequest struct {
	Attitude      int             `json:"attitude"`
	Type          int             `json:"type"`
	IsAutomated   bool            `json:"is_automated"`
	Label         *string         `json:"label"`
	Symbol        *string         `json:"symbol"`
	AppIdentifier *string         `json:"app_identifier"`
	IconID        *string         `json:"icon_id"`
	BackgroundID  *string         `json:"background_id"`
	Meta          json.RawMessage `json:"meta"`
	ClearedAt     *time.Time      `json:"cleared_at"`
}
type Status struct {
	ID            string          `json:"id"`
	Attitude      int             `json:"attitude"`
	IsOnline      bool            `json:"is_online"`
	IsCustomized  bool            `json:"is_customized"`
	IsIdle        bool            `json:"is_idle"`
	IdleSince     *time.Time      `json:"idle_since"`
	Type          int             `json:"type"`
	Label         *string         `json:"label"`
	Symbol        *string         `json:"symbol"`
	AppIdentifier *string         `json:"app_identifier"`
	IsAutomated   bool            `json:"is_automated"`
	Meta          json.RawMessage `json:"meta"`
	Icon          any             `json:"icon"`
	Background    any             `json:"background"`
	ClearedAt     *time.Time      `json:"cleared_at"`
	AccountID     string          `json:"account_id"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	DeletedAt     *time.Time      `json:"deleted_at"`
	LastSeenAt    *time.Time      `json:"last_seen_at"`
}

// Privacy is an owned extension; each field accepts public, friends, or private.
// Device identities/session identifiers are never exposed by this projection.
type Preferences struct {
	OnlineVisibility   string `json:"online_visibility"`
	LastSeenVisibility string `json:"last_seen_visibility"`
	StatusVisibility   string `json:"status_visibility"`
}

func DefaultPreferences() Preferences { return Preferences{"public", "friends", "public"} }

type Lease struct {
	ExpiresAt  time.Time `json:"expires_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}
type Repository interface {
	GetStatus(context.Context, string, string) (Status, error)
	SetStatus(context.Context, string, StatusRequest, bool) (Status, error)
	ClearStatus(context.Context, string, string) error
	GetPreferences(context.Context, string) (Preferences, error)
	SetPreferences(context.Context, string, Preferences) (Preferences, error)
	Heartbeat(context.Context, string, bool) (Lease, error)
	Disconnect(context.Context, string) error
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}

func ValidateRequest(v StatusRequest) error {
	if v.Type < Default || v.Type > Invisible || v.Attitude < -100 || v.Attitude > 100 {
		return ErrInvalid
	}
	for _, v := range []struct {
		p *string
		n int
	}{{v.Label, 1024}, {v.Symbol, 128}, {v.AppIdentifier, 4096}} {
		if v.p != nil && len([]rune(*v.p)) > v.n {
			return ErrInvalid
		}
	}
	if len(v.Meta) > 16384 {
		return ErrInvalid
	}
	if len(v.Meta) > 0 && string(v.Meta) != "null" {
		var m map[string]any
		if json.Unmarshal(v.Meta, &m) != nil || m == nil {
			return ErrInvalid
		}
	}
	if v.IsAutomated && (v.AppIdentifier == nil || *v.AppIdentifier == "") {
		return ErrInvalid
	}
	if v.IconID != nil || v.BackgroundID != nil {
		return ErrUnsupported
	}
	return nil
}
func ValidatePreferences(v Preferences) error {
	for _, s := range []string{v.OnlineVisibility, v.LastSeenVisibility, v.StatusVisibility} {
		if s != "public" && s != "friends" && s != "private" {
			return ErrInvalid
		}
	}
	return nil
}
