// SPDX-License-Identifier: AGPL-3.0-only
// Package pets implements an independently authored, deterministic virtual pet.
package pets

import (
	"context"
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
	ErrInvalid      = &Failure{"PET_INVALID", "Invalid pet request.", 400}
	ErrNotFound     = &Failure{"PET_NOT_FOUND", "Adopt a pet first.", 404}
	ErrConflict     = &Failure{"PET_CONFLICT", "Request or version conflict; refresh your pet.", 409}
	ErrUnavailable  = &Failure{"PET_UNAVAILABLE", "Pet service is unavailable.", 503}
	ErrCooldown     = &Failure{"PET_COOLDOWN", "This care action is cooling down.", 429}
	ErrLimit        = &Failure{"PET_DAILY_LIMIT", "Daily action limit reached.", 429}
	ErrRequirements = &Failure{"PET_REQUIREMENTS", "Not enough supplies, coins, energy, or quest progress.", 409}
)

// Command never contains a caller-selected account, clock, price or reward.
// ExpectedVersion is zero only for adoption. RequestID is unique per account.
type Command struct {
	RequestID       string `json:"request_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Kind            string `json:"kind"`
	Name            string `json:"name,omitempty"`
	Species         string `json:"species,omitempty"`
	ItemID          string `json:"item_id,omitempty"`
	Slot            string `json:"slot,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
}
type Stats struct {
	Satiety     int `json:"satiety"`
	Cleanliness int `json:"cleanliness"`
	Mood        int `json:"mood"`
	Energy      int `json:"energy"`
}
type Daily struct {
	Day          string          `json:"day"`
	Commands     int             `json:"commands"`
	RewardedCare int             `json:"rewarded_care"`
	Care         map[string]int  `json:"care"`
	Claimed      map[string]bool `json:"claimed"`
}
type State struct {
	Name       string               `json:"name"`
	Species    string               `json:"species"`
	Version    int64                `json:"version"`
	Stats      Stats                `json:"stats"`
	Experience int64                `json:"experience"`
	Level      int                  `json:"level"`
	Coins      int64                `json:"coins"`
	Inventory  map[string]int       `json:"inventory"`
	Equipment  map[string]string    `json:"equipment"`
	Daily      Daily                `json:"daily"`
	Cooldowns  map[string]time.Time `json:"cooldowns"`
	AdoptedAt  time.Time            `json:"adopted_at"`
	SettledAt  time.Time            `json:"settled_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
}
type Item struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Price    int64  `json:"price"`
	Slot     string `json:"slot,omitempty"`
	AssetKey string `json:"asset_key"`
}
type Species struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	AssetKey string `json:"asset_key"`
}
type Task struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	Target     int    `json:"target"`
	Coins      int64  `json:"coins"`
	Experience int64  `json:"experience"`
}
type Catalog struct {
	Species []Species `json:"species"`
	Items   []Item    `json:"items"`
	Tasks   []Task    `json:"tasks"`
}
type Repository interface {
	Get(context.Context, string) (State, error)
	Mutate(context.Context, string, Command) (State, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}
