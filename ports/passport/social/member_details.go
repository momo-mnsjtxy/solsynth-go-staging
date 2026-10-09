// SPDX-License-Identifier: AGPL-3.0-only
package social

import "time"

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
