// SPDX-License-Identifier: AGPL-3.0-only
package pets

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxBalance int64 = 1000000000
const MaxDailyCommands = 100
const MaxDailyRewardedCare = 20
const CareCooldown = 5 * time.Minute

// Definitions returns fresh slices so callers cannot change the authoritative catalog.
func Definitions() Catalog {
	return Catalog{
		Species: []Species{{"cloudling", "Cloudling", "pets/cloudling"}, {"foxlet", "Foxlet", "pets/foxlet"}, {"mossling", "Mossling", "pets/mossling"}},
		Items:   []Item{{"snack", "Snack", 2, "", "pets/snack"}, {"soap", "Soap", 2, "", "pets/soap"}, {"blue_scarf", "Blue scarf", 30, "neck", "pets/blue_scarf"}, {"leaf_hat", "Leaf hat", 50, "head", "pets/leaf_hat"}},
		Tasks:   []Task{{"breakfast", "feed", 1, 5, 10}, {"playtime", "play", 1, 5, 10}, {"care_four", "all", 4, 10, 20}},
	}
}
func validName(s string) bool {
	if s != strings.TrimSpace(s) || !utf8.ValidString(s) || utf8.RuneCountInString(s) < 1 || utf8.RuneCountInString(s) > 32 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}
func Validate(c Command) error {
	if len(c.RequestID) < 16 || len(c.RequestID) > 64 || c.ExpectedVersion < 0 || (c.Kind != "adopt" && c.ExpectedVersion == 0) {
		return ErrInvalid
	}
	for _, r := range c.RequestID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ErrInvalid
		}
	}
	switch c.Kind {
	case "adopt":
		found := false
		for _, s := range Definitions().Species {
			if s.ID == c.Species {
				found = true
			}
		}
		if !found || !validName(c.Name) || c.ExpectedVersion != 0 || c.ItemID != "" || c.Slot != "" || c.TaskID != "" {
			return ErrInvalid
		}
	case "rename":
		if !validName(c.Name) || c.Species != "" || c.ItemID != "" || c.Slot != "" || c.TaskID != "" {
			return ErrInvalid
		}
	case "feed", "wash", "play", "rest":
		if c.Name != "" || c.Species != "" || c.ItemID != "" || c.Slot != "" || c.TaskID != "" {
			return ErrInvalid
		}
	case "buy":
		if _, ok := item(c.ItemID); !ok || c.Name != "" || c.Species != "" || c.Slot != "" || c.TaskID != "" {
			return ErrInvalid
		}
	case "equip":
		if c.Name != "" || c.Species != "" || c.TaskID != "" || (c.Slot != "head" && c.Slot != "neck") {
			return ErrInvalid
		}
		if c.ItemID != "" {
			i, ok := item(c.ItemID)
			if !ok || i.Slot != c.Slot {
				return ErrInvalid
			}
		}
	case "claim":
		found := false
		for _, t := range Definitions().Tasks {
			if t.ID == c.TaskID {
				found = true
			}
		}
		if !found || c.Name != "" || c.Species != "" || c.ItemID != "" || c.Slot != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func item(id string) (Item, bool) {
	for _, i := range Definitions().Items {
		if i.ID == id {
			return i, true
		}
	}
	return Item{}, false
}
func clamp(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}
func clone(s State) State {
	inv := map[string]int{}
	for k, v := range s.Inventory {
		inv[k] = v
	}
	s.Inventory = inv
	eq := map[string]string{}
	for k, v := range s.Equipment {
		eq[k] = v
	}
	s.Equipment = eq
	cd := map[string]time.Time{}
	for k, v := range s.Cooldowns {
		cd[k] = v
	}
	s.Cooldowns = cd
	care := map[string]int{}
	for k, v := range s.Daily.Care {
		care[k] = v
	}
	s.Daily.Care = care
	claims := map[string]bool{}
	for k, v := range s.Daily.Claimed {
		claims[k] = v
	}
	s.Daily.Claimed = claims
	return s
}

// Settle is a pure read projection: elapsed whole minutes are charged once,
// residual seconds are preserved, and neglect never deletes a pet or its growth.
func Settle(s State, now time.Time) State {
	s = clone(s)
	now = now.UTC()
	if now.Before(s.UpdatedAt) {
		now = s.UpdatedAt
	}
	if now.After(s.SettledAt) {
		minutes := int64(now.Sub(s.SettledAt) / time.Minute)
		// Values saturate after 100 ticks; use a bounded conversion even after years away.
		ticks := int(minutes / 30)
		if ticks > 100 {
			ticks = 100
		}
		if ticks > 0 {
			s.Stats.Satiety = clamp(s.Stats.Satiety - ticks*2)
			s.Stats.Cleanliness = clamp(s.Stats.Cleanliness - ticks)
			s.Stats.Mood = clamp(s.Stats.Mood - ticks)
			s.Stats.Energy = clamp(s.Stats.Energy - ticks)
			// Sub saturates at roughly 292 years. Only the bounded decay uses
			// that elapsed duration; advance the anchor using its sub-tick phase
			// so a larger gap cannot be charged again after subsequent care.
			const period = 30 * time.Minute
			phase := s.SettledAt.Sub(s.SettledAt.Truncate(period))
			s.SettledAt = now.Truncate(period).Add(phase)
			if s.SettledAt.After(now) {
				s.SettledAt = s.SettledAt.Add(-period)
			}
		}
	}
	if day := now.Format("2006-01-02"); day > s.Daily.Day {
		s.Daily = Daily{Day: day, Care: map[string]int{}, Claimed: map[string]bool{}}
	}
	return s
}
func growth(s *State, xp, coins int64) {
	s.Experience += xp
	if s.Experience > MaxBalance {
		s.Experience = MaxBalance
	}
	s.Coins += coins
	if s.Coins > MaxBalance {
		s.Coins = MaxBalance
	}
	s.Level = 1 + int(s.Experience/100)
	if s.Level > 100 {
		s.Level = 100
	}
}

// Apply returns a new value, leaving the caller's state unchanged on failure.
// Persistence owns authentication, server time, serialization and idempotent replay.
func Apply(before *State, c Command, now time.Time) (State, error) {
	if err := Validate(c); err != nil {
		return State{}, err
	}
	now = now.UTC()
	if before == nil {
		if c.Kind != "adopt" {
			return State{}, ErrNotFound
		}
		return State{Name: c.Name, Species: c.Species, Version: 1, Stats: Stats{80, 80, 80, 80}, Level: 1, Coins: 20, Inventory: map[string]int{"snack": 5, "soap": 5}, Equipment: map[string]string{}, Cooldowns: map[string]time.Time{}, Daily: Daily{Day: now.Format("2006-01-02"), Commands: 1, Care: map[string]int{}, Claimed: map[string]bool{}}, AdoptedAt: now, SettledAt: now, UpdatedAt: now}, nil
	}
	if c.Kind == "adopt" || c.ExpectedVersion != before.Version || before.Version >= 1000000000000 {
		return State{}, ErrConflict
	}
	s := Settle(*before, now)
	if now.Before(s.UpdatedAt) {
		now = s.UpdatedAt
	}
	if s.Daily.Commands >= MaxDailyCommands {
		return State{}, ErrLimit
	}
	switch c.Kind {
	case "rename":
		s.Name = c.Name
	case "buy":
		i, _ := item(c.ItemID)
		limit := 99
		if i.Slot != "" {
			limit = 1
		}
		if s.Coins < i.Price || s.Inventory[i.ID] >= limit {
			return State{}, ErrRequirements
		}
		s.Coins -= i.Price
		s.Inventory[i.ID]++
	case "equip":
		if c.ItemID == "" {
			delete(s.Equipment, c.Slot)
		} else {
			if s.Inventory[c.ItemID] < 1 {
				return State{}, ErrRequirements
			}
			s.Equipment[c.Slot] = c.ItemID
		}
	case "claim":
		if s.Daily.Claimed[c.TaskID] {
			return State{}, ErrConflict
		}
		for _, t := range Definitions().Tasks {
			if t.ID == c.TaskID {
				progress := s.Daily.Care[t.Action]
				if t.Action == "all" {
					progress = 0
					for _, v := range s.Daily.Care {
						progress += v
					}
				}
				if progress < t.Target {
					return State{}, ErrRequirements
				}
				growth(&s, t.Experience, t.Coins)
				s.Daily.Claimed[t.ID] = true
			}
		}
	default:
		if now.Before(s.Cooldowns[c.Kind]) {
			return State{}, ErrCooldown
		}
		switch c.Kind {
		case "feed":
			if s.Inventory["snack"] < 1 || s.Stats.Satiety == 100 {
				return State{}, ErrRequirements
			}
			s.Inventory["snack"]--
			s.Stats.Satiety = clamp(s.Stats.Satiety + 30)
		case "wash":
			if s.Inventory["soap"] < 1 || s.Stats.Cleanliness == 100 {
				return State{}, ErrRequirements
			}
			s.Inventory["soap"]--
			s.Stats.Cleanliness = clamp(s.Stats.Cleanliness + 40)
		case "play":
			if s.Stats.Energy < 10 || s.Stats.Mood == 100 {
				return State{}, ErrRequirements
			}
			s.Stats.Energy -= 10
			s.Stats.Satiety = clamp(s.Stats.Satiety - 5)
			s.Stats.Mood = clamp(s.Stats.Mood + 25)
		case "rest":
			if s.Stats.Energy == 100 {
				return State{}, ErrRequirements
			}
			s.Stats.Energy = clamp(s.Stats.Energy + 30)
		}
		s.Cooldowns[c.Kind] = now.Add(CareCooldown)
		s.Daily.Care[c.Kind]++
		if s.Daily.RewardedCare < MaxDailyRewardedCare {
			growth(&s, 5, 2)
			s.Daily.RewardedCare++
		}
	}
	s.Daily.Commands++
	s.Version++
	s.UpdatedAt = now
	return s, nil
}
