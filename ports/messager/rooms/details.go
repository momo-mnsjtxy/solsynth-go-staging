// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"database/sql"
	"regexp"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/presence"
	"strings"
	"time"
	"unicode/utf16"
)

// ProfileRequest follows the authored controller: room identity customizes Nick
// only. Account artwork stays under the canonical account/media lifecycle.
type ProfileRequest struct {
	Nick *string `json:"nick"`
}
type OnlineMembers struct {
	OnlineCount         int                `json:"online_count"`
	DirectMessageStatus *presence.Status   `json:"direct_message_status"`
	OnlineUserNames     []string           `json:"online_user_names"`
	OnlineAccounts      []accounts.Account `json:"online_accounts"`
}
type ModerationDelete struct {
	Reason *string `json:"reason"`
}
type ModerationTimeout struct {
	DurationMinutes int     `json:"duration_minutes"`
	Reason          *string `json:"reason"`
}
type ModerationTimeoutResult struct {
	Success      bool      `json:"success"`
	Message      string    `json:"message"`
	TimeoutUntil time.Time `json:"timeout_until"`
}
type DetailRepository interface {
	PublicBySlug(context.Context, string, string, string) (Room, error)
	OnlineMembers(context.Context, string, string) (OnlineMembers, error)
	UpdateProfile(context.Context, string, string, ProfileRequest) (Member, error)
	ClearProfile(context.Context, string, string) (Member, error)
	ModerateDelete(context.Context, string, string, string, ModerationDelete) error
	ModerateTimeout(context.Context, string, string, string, ModerationTimeout) (ModerationTimeoutResult, error)
}

// ProfileEntitlement must inspect current canonical paid subscriptions in this
// transaction using an explicit operator mapping. It must never seed grants.
type ProfileEntitlement interface {
	RequireChatProfile(context.Context, *sql.DB, *sql.Tx, string, time.Time) error
}

var slugShape = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]*[a-z0-9])?$`)

func NormalizeSlug(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if len(utf16.Encode([]rune(*value))) > 128 {
		return nil, ErrInvalid
	}
	v := strings.ToLower(strings.TrimSpace(*value))
	if v == "" {
		return nil, nil
	}
	if !slugShape.MatchString(v) {
		return nil, ErrInvalid
	}
	return &v, nil
}
func ValidateProfile(q ProfileRequest) error {
	if q.Nick != nil && (!validText(*q.Nick, 1024) || len(utf16.Encode([]rune(*q.Nick))) > 1024) {
		return ErrInvalid
	}
	return nil
}
func ValidateModerationReason(reason *string) error {
	if reason != nil && (!validText(*reason, 4096) || len(utf16.Encode([]rune(*reason))) > 4096) {
		return ErrInvalid
	}
	return nil
}
