// SPDX-License-Identifier: AGPL-3.0-only
// Adapted from licensed DysonNetwork ChatService.cs at revision
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-07.
package messager

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// PlaceholderKind is an internal enum, not the unresolved shared helper's wire constants.
type PlaceholderKind uint8

const (
	PlaceholderUnknown PlaceholderKind = iota
	PlaceholderStreaming
	PlaceholderUploading
)

const PlaceholderTTL = time.Hour

var (
	ErrPlaceholderKind    = errors.New("invalid placeholder kind")
	ErrNotPlaceholder     = errors.New("message is not a placeholder")
	ErrPlaceholderDeleted = errors.New("placeholder has been deleted")
	ErrPlaceholderScope   = errors.New("existing placeholder does not match room and sender")
)

// ResolvedPlaceholder contains values already resolved by a future licensed
// adapter. It deliberately does not implement missing shared helper parsing.
// A nil *ResolvedPlaceholder means the message is not a placeholder.
type ResolvedPlaceholder struct {
	Kind        PlaceholderKind
	Content     *string
	Progress    *float64
	ExpiresAtMS *int64
}

// PlaceholderSnapshot is an internal transition projection, not an HTTP DTO.
// OtherMeta carries metadata excluding the four placeholder keys. Attachments
// are opaque JSON: this package never interprets excluded shared file types.
// No IDs, audit timestamps, room sequences or authorization are manufactured.
type PlaceholderSnapshot struct {
	RoomID, SenderID string
	Type             string
	Content          *string
	Placeholder      *ResolvedPlaceholder
	OtherMeta        map[string]json.RawMessage
	Attachments      []json.RawMessage
	DeletedAt        *time.Time
	UpdatedAt        time.Time
}

// PlaceholderSendTransition returns the changed prior snapshot (if any) and a
// new unsaved snapshot. The caller selects the first matching active record,
// persists all work and performs source-required deliveries independently.
type PlaceholderSendTransition struct {
	Previous *PlaceholderSnapshot
	Created  PlaceholderSnapshot
}

func SendPlaceholder(roomID, senderID string, kind PlaceholderKind, existing *PlaceholderSnapshot, now time.Time) (PlaceholderSendTransition, error) {
	if kind != PlaceholderStreaming && kind != PlaceholderUploading {
		return PlaceholderSendTransition{}, ErrPlaceholderKind
	}
	result := PlaceholderSendTransition{}
	if existing != nil {
		if existing.RoomID != roomID || existing.SenderID != senderID {
			return result, ErrPlaceholderScope
		}
		if existing.Type != "placeholder" {
			return result, ErrNotPlaceholder
		}
		if err := activePlaceholder(*existing); err != nil {
			return result, err
		}
		previous := clonePlaceholder(*existing)
		if previous.Placeholder.Content != nil && strings.TrimSpace(*previous.Placeholder.Content) != "" {
			previous, _ = FinalizePlaceholder(previous, previous.Placeholder.Content, nil, nil)
		} else {
			previous = ExpirePlaceholder(previous, now)
		}
		result.Previous = &previous
	}
	expiry := now.Add(PlaceholderTTL).UnixMilli()
	state := &ResolvedPlaceholder{Kind: kind, ExpiresAtMS: &expiry}
	if kind == PlaceholderStreaming {
		content := ""
		state.Content = &content
	} else {
		progress := 0.0
		state.Progress = &progress
	}
	result.Created = PlaceholderSnapshot{RoomID: roomID, SenderID: senderID, Type: "placeholder", Placeholder: state, Attachments: []json.RawMessage{}}
	return result, nil
}

// UpdatePlaceholder refreshes TTL even for an empty update or an unknown kind.
// Elapsed TTL alone does not block updates: upstream checks DeletedAt only.
// IEEE NaN progress is preserved, as by Math.Clamp; serialization is separate.
func UpdatePlaceholder(message PlaceholderSnapshot, chunk *string, progress *float64, now time.Time) (PlaceholderSnapshot, error) {
	if err := activePlaceholder(message); err != nil {
		return PlaceholderSnapshot{}, err
	}
	result := clonePlaceholder(message)
	state := result.Placeholder
	if state.Kind == PlaceholderStreaming && chunk != nil {
		content := ""
		if state.Content != nil {
			content = *state.Content
		}
		content += *chunk
		state.Content = &content
	}
	if state.Kind == PlaceholderUploading && progress != nil {
		value := *progress
		if value < 0 {
			value = 0
		}
		if value > 1 {
			value = 1
		}
		state.Progress = &value
	}
	expiry := now.Add(PlaceholderTTL).UnixMilli()
	state.ExpiresAtMS = &expiry
	return result, nil
}

// FinalizePlaceholder returns the pre-save text state. A nil content uses the
// accumulated content; an explicit empty string replaces it. Nil attachments
// preserve existing ones, whereas a nonnil empty slice clears them. extraMeta
// merges AFTER placeholder keys are removed and may reintroduce those keys.
func FinalizePlaceholder(message PlaceholderSnapshot, content *string, attachments []json.RawMessage, extraMeta map[string]json.RawMessage) (PlaceholderSnapshot, error) {
	if err := activePlaceholder(message); err != nil {
		return PlaceholderSnapshot{}, err
	}
	result := clonePlaceholder(message)
	resolved := ""
	if content != nil {
		resolved = *content
	} else if result.Placeholder.Content != nil {
		resolved = *result.Placeholder.Content
	}
	result.Type = "text"
	result.Content = &resolved
	result.Placeholder = nil
	if attachments != nil {
		result.Attachments = clonePlaceholderJSON(attachments)
	}
	for _, key := range []string{"placeholder_kind", "placeholder_content", "placeholder_progress", "placeholder_expires_at"} {
		delete(result.OtherMeta, key)
	}
	for key, value := range extraMeta {
		if result.OtherMeta == nil {
			result.OtherMeta = make(map[string]json.RawMessage)
		}
		result.OtherMeta[key] = append(json.RawMessage(nil), value...)
	}
	if len(result.OtherMeta) == 0 {
		result.OtherMeta = nil
	}
	return result, nil
}

// ExpirePlaceholder mirrors the unconditional source method, including repeated
// expiry and non-placeholder inputs. Callers must enforce their own access rules.
func ExpirePlaceholder(message PlaceholderSnapshot, now time.Time) PlaceholderSnapshot {
	result := clonePlaceholder(message)
	result.DeletedAt = &now
	result.UpdatedAt = now
	return result
}

// PlaceholderIsStale is only a selection predicate, not a scheduled job.
func PlaceholderIsStale(message PlaceholderSnapshot, now time.Time) bool {
	return message.Type == "placeholder" && message.Placeholder != nil && message.DeletedAt == nil && message.Placeholder.ExpiresAtMS != nil && *message.Placeholder.ExpiresAtMS <= now.UnixMilli()
}
func activePlaceholder(message PlaceholderSnapshot) error {
	if message.Placeholder == nil {
		return ErrNotPlaceholder
	}
	if message.DeletedAt != nil {
		return ErrPlaceholderDeleted
	}
	return nil
}
func clonePlaceholderValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func clonePlaceholderJSON(values []json.RawMessage) []json.RawMessage {
	if values == nil {
		return nil
	}
	result := make([]json.RawMessage, len(values))
	for i, value := range values {
		result[i] = append(json.RawMessage(nil), value...)
	}
	return result
}
func clonePlaceholder(message PlaceholderSnapshot) PlaceholderSnapshot {
	result := message
	result.Content = clonePlaceholderValue(message.Content)
	result.DeletedAt = clonePlaceholderValue(message.DeletedAt)
	if message.Placeholder != nil {
		state := *message.Placeholder
		state.Content = clonePlaceholderValue(state.Content)
		state.Progress = clonePlaceholderValue(state.Progress)
		state.ExpiresAtMS = clonePlaceholderValue(state.ExpiresAtMS)
		result.Placeholder = &state
	}
	result.Attachments = clonePlaceholderJSON(message.Attachments)
	if message.OtherMeta != nil {
		result.OtherMeta = make(map[string]json.RawMessage, len(message.OtherMeta))
		for key, value := range message.OtherMeta {
			result.OtherMeta[key] = append(json.RawMessage(nil), value...)
		}
	}
	return result
}
