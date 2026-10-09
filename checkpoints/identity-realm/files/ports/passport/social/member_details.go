// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	"unicode/utf8"
)

// RealmLabel follows the retained authored controller, including the text icon.
// Icon is display text, never a media identifier or a URL to fetch.
type RealmLabel struct {
	ID                 string    `json:"id"`
	RealmID            string    `json:"realm_id"`
	Name               string    `json:"name"`
	Description        *string   `json:"description"`
	Color              *string   `json:"color"`
	Icon               *string   `json:"icon"`
	CreatedByAccountID string    `json:"created_by_account_id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// MemberProgression deliberately distinguishes recorded, supported sources from
// total legacy experience. A level exists only under explicit owned thresholds.
type MemberProgression struct {
	RecordedExperience int64    `json:"recorded_experience"`
	ExperienceComplete bool     `json:"experience_complete"`
	Sources            []string `json:"sources"`
	Level              *int     `json:"level"`
	LevelPolicy        *string  `json:"level_policy"`
	LevelFloor         *int64   `json:"level_floor"`
	NextLevelAt        *int64   `json:"next_level_at"`
}

type MemberFilter struct {
	Offset, Take                  int
	AccountName, LabelID, OrderBy string
	WithStatus                    bool
}
type MemberQueryRepository interface {
	MembersFiltered(context.Context, string, string, MemberFilter) ([]Member, int64, error)
}

func ValidateMemberFilter(f MemberFilter) error {
	if e := ValidatePage(f.Offset, f.Take); e != nil {
		return e
	}
	if len(f.AccountName) > 256 || strings.ContainsRune(f.AccountName, 0) || !utf8.ValidString(f.AccountName) || f.LabelID != "" && !authority.ValidID(f.LabelID) {
		return ErrInvalid
	}
	switch f.OrderBy {
	case "", "experience_desc", "joined_at_asc", "name_asc", "role_desc":
		return nil
	}
	return ErrInvalid
}
