// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"context"
	"encoding/hex"
	"math"
	"strings"
	"time"
)

const NearbySlotSeconds = 60
const NearbyMaxSlots = 10
const NearbyServiceUUID = "87ef13c2-9485-4d9f-ace2-c9356049e711"

type PresenceRequest struct {
	DeviceID      string `json:"device_id"`
	Discoverable  *bool  `json:"discoverable"`
	FriendOnly    *bool  `json:"friend_only"`
	Capabilities  int    `json:"capabilities"`
	PrefetchSlots int    `json:"prefetch_slots"`
}
type PresenceToken struct {
	Slot      int64     `json:"slot"`
	Token     string    `json:"token"`
	ValidFrom time.Time `json:"valid_from"`
	ValidTo   time.Time `json:"valid_to"`
}
type PresenceTokens struct {
	ServiceUUID     string          `json:"service_uuid"`
	SlotDurationSec int             `json:"slot_duration_sec"`
	Tokens          []PresenceToken `json:"tokens"`
}
type Observation struct {
	Token       string     `json:"token"`
	Slot        int64      `json:"slot"`
	AvgRSSI     int        `json:"avg_rssi"`
	SeenCount   int        `json:"seen_count"`
	DurationMS  int64      `json:"duration_ms"`
	FirstSeenAt *time.Time `json:"first_seen_at"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
}
type NearbyPeer struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	IsFriend    bool      `json:"is_friend"`
	CanInvite   bool      `json:"can_invite"`
	Visibility  string    `json:"visibility"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}
type NearbyRepository interface {
	IssuePresenceTokens(context.Context, string, PresenceRequest) (PresenceTokens, error)
	ResolveNearby(context.Context, string, []Observation) ([]NearbyPeer, error)
}

func ValidatePresence(v PresenceRequest) error {
	if strings.TrimSpace(v.DeviceID) == "" || !Text(&v.DeviceID, 256) || v.Capabilities < 0 || v.Capabilities > 65535 || v.PrefetchSlots < 0 || v.PrefetchSlots > NearbyMaxSlots {
		return ErrInvalid
	}
	return nil
}
func ValidateObservations(v []Observation, now time.Time) error {
	if len(v) < 1 || len(v) > 100 {
		return ErrInvalid
	}
	for _, o := range v {
		b, e := hex.DecodeString(o.Token)
		if e != nil || len(b) != 16 || o.Token != strings.ToLower(o.Token) || o.Slot < 0 || o.Slot > math.MaxInt64/NearbySlotSeconds-1 || o.AvgRSSI < -127 || o.AvgRSSI > 0 || o.SeenCount < 1 || o.SeenCount > 10000 || o.DurationMS < 0 || o.DurationMS > 600000 {
			return ErrInvalid
		}
		start := time.Unix(o.Slot*NearbySlotSeconds, 0)
		if o.FirstSeenAt != nil && (o.FirstSeenAt.Before(start) || o.FirstSeenAt.After(now) || !o.FirstSeenAt.Before(start.Add(NearbySlotSeconds*time.Second))) {
			return ErrInvalid
		}
		if o.LastSeenAt != nil && (o.LastSeenAt.Before(start) || o.LastSeenAt.After(now) || !o.LastSeenAt.Before(start.Add(NearbySlotSeconds*time.Second)) || o.FirstSeenAt != nil && o.LastSeenAt.Before(*o.FirstSeenAt)) {
			return ErrInvalid
		}
	}
	return nil
}
