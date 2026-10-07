// SPDX-License-Identifier: AGPL-3.0-only
// Modified 2026-10-07: Go port from Solsynth/DysonNetwork
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c, with licensed Solian client
// ffd0838e5f04cb40bea311a39118ebeedad210c1 JSON projections; see README.md.
//
// Package board is a source-derived, transport-independent Passport board port.
// No HTTP handler, permission evaluator, protobuf adapter or database driver is
// included. See README.md for compatibility boundaries and licensed provenance.
package board

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Kind int

const (
	Prebuilt Kind = iota
	CustomApp
)

type Payload map[string]any

// Item is the licensed Solian client's board-item projection plus its owner.
// Adapters must parse UUIDs and supply canonical UUID strings. This internal
// model is not claimed to be the complete legacy HTTP response entity.
type Item struct {
	ID                 string  `json:"id,omitempty"`
	AccountID          string  `json:"-"`
	Order              int32   `json:"order"`
	Kind               Kind    `json:"kind"`
	WidgetKey          *string `json:"widget_key,omitempty"`
	CustomAppID        *string `json:"custom_app_id,omitempty"`
	CustomAppWidgetKey *string `json:"custom_app_widget_key,omitempty"`
	IsEnabled          bool    `json:"is_enabled"`
	Payload            Payload `json:"payload"`
}

// Repository owns persistence, auditing and soft-delete filtering. Returned
// values must be detached. Replace atomically deletes and inserts for exactly
// one account, committing before return; errors must leave the old board intact.
// FindFirstWidget preserves the upstream adapter's case-insensitive lookup and
// its first-match ordering. An adapter must not invent a deterministic choice
// when the old database query had no explicit ordering.
type Repository interface {
	List(context.Context, string) ([]Item, error)
	Replace(context.Context, string, []Item) error
	FindByID(context.Context, string, string) (Item, error)
	FindFirstWidget(context.Context, string, string, string) (Item, error)
	Save(context.Context, Item) error
}

type Widget struct {
	Key           string
	Enabled       bool
	AllowMultiple bool
}

// Catalog is an authoritative app/widget adapter. Lookup must retain the
// existing board-scope checks and error semantics.
type Catalog interface {
	Lookup(context.Context, string, string) (*Widget, error)
	Validate(context.Context, string, string, Payload) (Payload, error)
}

// Normalizer supplies the authoritative universal envelope validation behavior.
// The unknown implementation is intentionally not replaced by a permissive stub.
type Normalizer interface {
	Normalize(context.Context, Payload) (Payload, error)
}

type Cache interface {
	InvalidateGroup(context.Context, string) error
}

type Service struct {
	Repository Repository
	Catalog    Catalog
	Normalizer Normalizer
	Cache      Cache
	NewID      func() (string, error)
}

var (
	ErrDependency = errors.New("board: required adapter unavailable")
	ErrNotFound   = errors.New("Board item not found.")
)

// DependencyError identifies a missing adapter and supports errors.Is.
type DependencyError struct{ Adapter string }

func (e *DependencyError) Error() string { return "board: " + e.Adapter + " adapter unavailable" }
func (e *DependencyError) Unwrap() error { return ErrDependency }

func dependency(name string) error { return &DependencyError{Adapter: name} }

func (s Service) List(ctx context.Context, accountID string) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Repository == nil {
		return nil, dependency("repository")
	}
	items, err := s.Repository.List(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := copyItems(items)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

// Replace preserves app-owned payloads, validates all items before mutation,
// delegates one atomic replacement, then invalidates the account cache group.
// A cache error after commit is returned without pretending rollback occurred.
func (s Service) Replace(ctx context.Context, accountID string, input []Item) ([]Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Repository == nil {
		return nil, dependency("repository")
	}
	if s.Cache == nil {
		return nil, dependency("cache")
	}
	old, err := s.Repository.List(ctx, accountID)
	if err != nil {
		return nil, err
	}
	items := copyItems(input)
	if err = s.validateLayout(ctx, items); err != nil {
		return nil, err
	}
	existing := map[string]Item{}
	remaining := map[string][]Item{}
	for _, item := range old {
		if item.Kind != CustomApp {
			continue
		}
		existing[item.ID] = item
		key := instanceKey(item.CustomAppID, item.CustomAppWidgetKey)
		remaining[key] = append(remaining[key], item)
	}
	for i := range items {
		item := &items[i]
		if item.Kind == CustomApp {
			key := instanceKey(item.CustomAppID, item.CustomAppWidgetKey)
			prior, ok := existing[item.ID]
			if item.ID != "" && item.ID != zeroID && ok && samePointer(prior.CustomAppID, item.CustomAppID) && samePointerFold(prior.CustomAppWidgetKey, item.CustomAppWidgetKey) {
				item.Payload = copyPayload(prior.Payload)
				candidates := remaining[key]
				kept := candidates[:0]
				for _, candidate := range candidates {
					if candidate.ID != item.ID {
						kept = append(kept, candidate)
					}
				}
				remaining[key] = kept
			} else if candidates := remaining[key]; len(candidates) > 0 {
				item.Payload = copyPayload(candidates[0].Payload)
				remaining[key] = candidates[1:]
			} else {
				item.Payload = Payload{}
			}
		}
		if item.ID == "" || item.ID == zeroID {
			newID := s.NewID
			if newID == nil {
				newID = randomID
			}
			item.ID, err = newID()
			if err != nil {
				return nil, err
			}
		}
		item.AccountID = accountID
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err = s.Repository.Replace(ctx, accountID, items); err != nil {
		return nil, err
	}
	if err = s.Cache.InvalidateGroup(ctx, "account:"+accountID); err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	return items, nil
}

func (s Service) validateLayout(ctx context.Context, items []Item) error {
	orders := map[int32]bool{}
	for _, item := range items {
		if orders[item.Order] {
			return fmt.Errorf("Duplicate board order '%d' is not allowed.", item.Order)
		}
		orders[item.Order] = true
	}
	singletons := map[string]bool{}
	for i := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := &items[i]
		switch item.Kind {
		case Prebuilt:
			item.CustomAppID = nil
			item.CustomAppWidgetKey = nil
		case CustomApp:
			if item.CustomAppID == nil {
				return errors.New("Custom app board widgets require custom_app_id.")
			}
			if item.CustomAppWidgetKey == nil || strings.TrimSpace(*item.CustomAppWidgetKey) == "" {
				return errors.New("Custom app board widgets require custom_app_widget_key.")
			}
			if s.Catalog == nil {
				return dependency("catalog")
			}
			widget, err := s.Catalog.Lookup(ctx, *item.CustomAppID, *item.CustomAppWidgetKey)
			if err != nil {
				return err
			}
			if widget == nil {
				return errors.New("Board widget is not configured for this app.")
			}
			if !widget.Enabled {
				return errors.New("Board widget is disabled for this app.")
			}
			if !widget.AllowMultiple {
				key := instanceKey(item.CustomAppID, item.CustomAppWidgetKey)
				if singletons[key] {
					return fmt.Errorf("Custom app widget '%s:%s' can only appear once.", *item.CustomAppID, *item.CustomAppWidgetKey)
				}
				singletons[key] = true
			}
			item.WidgetKey = nil
			if strings.TrimSpace(widget.Key) != "" {
				key := widget.Key
				item.CustomAppWidgetKey = &key
			}
			item.Payload = Payload{}
		default:
			return fmt.Errorf("Unsupported board item kind '%d'.", item.Kind)
		}
	}
	return nil
}

func (s Service) UpdateAppPayload(ctx context.Context, accountID, itemID, appID, widgetKey string, payload Payload) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if s.Repository == nil {
		return Item{}, dependency("repository")
	}
	if s.Cache == nil {
		return Item{}, dependency("cache")
	}
	if s.Normalizer == nil {
		return Item{}, dependency("normalizer")
	}
	var item Item
	var err error
	if itemID == "" || itemID == zeroID {
		item, err = s.Repository.FindFirstWidget(ctx, accountID, appID, widgetKey)
	} else {
		item, err = s.Repository.FindByID(ctx, accountID, itemID)
	}
	if err != nil {
		return Item{}, err
	}
	if item.Kind != CustomApp {
		return Item{}, errors.New("Only custom app board items can be updated through this endpoint.")
	}
	if item.CustomAppID == nil || *item.CustomAppID != appID {
		return Item{}, errors.New("Board item does not belong to the specified custom app.")
	}
	if item.CustomAppWidgetKey == nil || !strings.EqualFold(*item.CustomAppWidgetKey, widgetKey) {
		return Item{}, errors.New("Board item does not belong to the specified custom app widget.")
	}
	normalized, err := s.Normalizer.Normalize(ctx, copyPayload(payload))
	if err != nil {
		return Item{}, err
	}
	item.Payload = copyPayload(normalized)
	return s.save(ctx, item)
}

func (s Service) UpdateAdminPayload(ctx context.Context, accountID, itemID string, payload Payload) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if s.Repository == nil {
		return Item{}, dependency("repository")
	}
	if s.Cache == nil {
		return Item{}, dependency("cache")
	}
	item, err := s.Repository.FindByID(ctx, accountID, itemID)
	if err != nil {
		return Item{}, err
	}
	var normalized Payload
	switch item.Kind {
	case Prebuilt:
		if s.Normalizer == nil {
			return Item{}, dependency("normalizer")
		}
		normalized, err = s.Normalizer.Normalize(ctx, copyPayload(payload))
	case CustomApp:
		if item.CustomAppID == nil {
			return Item{}, errors.New("Custom app board item is missing custom_app_id.")
		}
		if s.Catalog == nil {
			return Item{}, dependency("catalog")
		}
		key := ""
		if item.CustomAppWidgetKey != nil {
			key = *item.CustomAppWidgetKey
		}
		normalized, err = s.Catalog.Validate(ctx, *item.CustomAppID, key, copyPayload(payload))
	default:
		return Item{}, fmt.Errorf("Unsupported board item kind '%d'.", item.Kind)
	}
	if err != nil {
		return Item{}, err
	}
	item.Payload = copyPayload(normalized)
	return s.save(ctx, item)
}

func (s Service) save(ctx context.Context, item Item) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if err := s.Repository.Save(ctx, item); err != nil {
		return Item{}, err
	}
	if err := s.Cache.InvalidateGroup(ctx, "account:"+item.AccountID); err != nil {
		return Item{}, err
	}
	return item, nil
}

const zeroID = "00000000-0000-0000-0000-000000000000"

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func instanceKey(app, key *string) string {
	a, k := "", ""
	if app != nil {
		a = *app
	}
	if key != nil {
		k = *key
	}
	return strings.ToLower(a + ":" + k)
}
func samePointer(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func samePointerFold(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && strings.EqualFold(*a, *b)
}
func copyItems(items []Item) []Item {
	out := make([]Item, len(items))
	for i, item := range items {
		out[i] = item
		out[i].WidgetKey = copyString(item.WidgetKey)
		out[i].CustomAppID = copyString(item.CustomAppID)
		out[i].CustomAppWidgetKey = copyString(item.CustomAppWidgetKey)
		out[i].Payload = copyPayload(item.Payload)
	}
	return out
}
func copyString(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}
func copyPayload(p Payload) Payload {
	out := Payload{}
	for k, v := range p {
		out[k] = copyValue(v)
	}
	return out
}
func copyValue(v any) any {
	switch x := v.(type) {
	case Payload:
		return copyPayload(x)
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[k] = copyValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = copyValue(v)
		}
		return out
	default:
		return v
	}
}
