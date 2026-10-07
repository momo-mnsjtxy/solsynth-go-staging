// SPDX-License-Identifier: AGPL-3.0-only
// Derived from the pinned PostWatchService.cs and Models/PostWatch.cs.
package sphere

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type WatchSource string

const (
	WatchBookmark WatchSource = "bookmark"
	WatchReaction WatchSource = "reaction"
	WatchReply    WatchSource = "reply"
)

type WatchEvent string

const (
	WatchReactions WatchEvent = "reactions"
	WatchReplies   WatchEvent = "replies"
	WatchChains    WatchEvent = "chains"
	WatchForwards  WatchEvent = "forwards"
	WatchEdits     WatchEvent = "edits"
)

var watchSources = [...]WatchSource{WatchBookmark, WatchReaction, WatchReply}

type WatchPreference struct {
	AccountID       string      `json:"-"`
	Source          WatchSource `json:"source"`
	NotifyReactions bool        `json:"notify_reactions"`
	NotifyReplies   bool        `json:"notify_replies"`
	NotifyChains    bool        `json:"notify_chains"`
	NotifyForwards  bool        `json:"notify_forwards"`
	NotifyEdits     bool        `json:"notify_edits"`
}

func DefaultWatchPreference(accountID string, source WatchSource) WatchPreference {
	bookmark := source == WatchBookmark
	return WatchPreference{AccountID: accountID, Source: source, NotifyReactions: bookmark, NotifyReplies: bookmark, NotifyChains: true, NotifyForwards: bookmark, NotifyEdits: true}
}

func (p WatchPreference) Allows(event WatchEvent) bool {
	switch event {
	case WatchReactions:
		return p.NotifyReactions
	case WatchReplies:
		return p.NotifyReplies
	case WatchChains:
		return p.NotifyChains
	case WatchForwards:
		return p.NotifyForwards
	case WatchEdits:
		return p.NotifyEdits
	default:
		return false
	}
}

type WatchPreferenceUpdate struct {
	Source                                                                    WatchSource
	NotifyReactions, NotifyReplies, NotifyChains, NotifyForwards, NotifyEdits *bool
}

// LoadPreferences returns live rows for exactly the requested accounts.
// SavePreferences atomically upserts the supplied account/source rows, retaining
// unspecified rows. Implementations must reject cross-account writes and
// duplicate account/source rows and persist durably before reporting success.
type WatchPreferenceRepository interface {
	LoadPreferences(context.Context, []string) ([]WatchPreference, error)
	SavePreferences(context.Context, string, []WatchPreference) error
}

// WatchPreferencePatcher is implemented by transactional repositories. The
// complete load/merge/save must be atomic across all repository instances;
// unspecified fields and sources are retained. Return effective preferences
// only after commit. Adapters must enforce account isolation and validate input.
type WatchPreferencePatcher interface {
	PatchPreferences(context.Context, string, []WatchPreferenceUpdate) ([]WatchPreference, error)
}

type WatchAccounts struct{ Bookmarkers, Reactors, Repliers []string }

// WatchAccounts must query live bookmarks/reactions/reply posts. Exclude reactions
// without accounts and replies without publisher accounts. ChainPostIDs returns
// the head itself if present plus posts whose ChainedPostId equals the head.
type WatcherRepository interface {
	WatchAccounts(context.Context, []string) (WatchAccounts, error)
	ChainPostIDs(context.Context, string) ([]string, error)
}

// WatchService serializes preference updates within one instance. Repositories
// shared across replicas must additionally provide transaction/optimistic-lock
// semantics around the complete read/merge/save operation. Do not copy a used
// WatchService, because it contains a mutex.
type WatchService struct {
	Repository WatchPreferenceRepository
	updateMu   sync.Mutex
}

func validWatchSource(s WatchSource) bool {
	return s == WatchBookmark || s == WatchReaction || s == WatchReply
}

func preferenceMap(rows []WatchPreference) (map[string]map[WatchSource]WatchPreference, error) {
	result := make(map[string]map[WatchSource]WatchPreference)
	for _, p := range rows {
		if result[p.AccountID] == nil {
			result[p.AccountID] = make(map[WatchSource]WatchPreference)
		}
		if _, exists := result[p.AccountID][p.Source]; exists {
			return nil, errors.New("duplicate account/source preference")
		}
		result[p.AccountID][p.Source] = p
	}
	return result, nil
}

func (s *WatchService) load(ctx context.Context, ids []string) (map[string]map[WatchSource]WatchPreference, error) {
	if s == nil || s.Repository == nil {
		return nil, errors.New("watch preference repository is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.Repository.LoadPreferences(ctx, ids)
	if err != nil {
		return nil, err
	}
	return preferenceMap(rows)
}

func (s *WatchService) GetEffectivePreferences(ctx context.Context, accountID string) ([]WatchPreference, error) {
	stored, err := s.load(ctx, []string{accountID})
	if err != nil {
		return nil, err
	}
	result := make([]WatchPreference, 0, len(watchSources))
	for _, source := range watchSources {
		p, ok := stored[accountID][source]
		if !ok {
			p = DefaultWatchPreference(accountID, source)
		}
		result = append(result, p)
	}
	return result, nil
}

func (s *WatchService) UpdatePreferences(ctx context.Context, accountID string, updates []WatchPreferenceUpdate) ([]WatchPreference, error) {
	if s == nil {
		return nil, errors.New("watch preference repository is required")
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if len(updates) == 0 {
		return s.GetEffectivePreferences(ctx, accountID)
	}
	seen := make(map[WatchSource]bool)
	for _, u := range updates {
		if !validWatchSource(u.Source) || seen[u.Source] {
			return nil, fmt.Errorf("invalid or duplicate watch source %q", u.Source)
		}
		seen[u.Source] = true
	}
	if patcher, ok := s.Repository.(WatchPreferencePatcher); ok {
		return patcher.PatchPreferences(ctx, accountID, updates)
	}
	stored, err := s.load(ctx, []string{accountID})
	if err != nil {
		return nil, err
	}
	rows := make([]WatchPreference, 0, len(updates))
	for _, u := range updates {
		p, ok := stored[accountID][u.Source]
		if !ok {
			p = DefaultWatchPreference(accountID, u.Source)
		}
		if u.NotifyReactions != nil {
			p.NotifyReactions = *u.NotifyReactions
		}
		if u.NotifyReplies != nil {
			p.NotifyReplies = *u.NotifyReplies
		}
		if u.NotifyChains != nil {
			p.NotifyChains = *u.NotifyChains
		}
		if u.NotifyForwards != nil {
			p.NotifyForwards = *u.NotifyForwards
		}
		if u.NotifyEdits != nil {
			p.NotifyEdits = *u.NotifyEdits
		}
		rows = append(rows, p)
	}
	if err = s.Repository.SavePreferences(ctx, accountID, rows); err != nil {
		return nil, err
	}
	return s.GetEffectivePreferences(ctx, accountID)
}

// ResolveWatcherAccountIDs returns the ordered source union, once per account,
// when at least one source allows the event. It does not send notifications.
func (s *WatchService) ResolveWatcherAccountIDs(ctx context.Context, repository WatcherRepository, postIDs []string, event WatchEvent) ([]string, error) {
	result := []string{}
	if len(postIDs) == 0 {
		return result, nil
	}
	if repository == nil {
		return nil, errors.New("watcher repository is required")
	}
	accounts, err := repository.WatchAccounts(ctx, postIDs)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	sources := make(map[string]map[WatchSource]bool)
	for i, group := range [][]string{accounts.Bookmarkers, accounts.Reactors, accounts.Repliers} {
		for _, id := range group {
			if sources[id] == nil {
				sources[id] = make(map[WatchSource]bool)
				ids = append(ids, id)
			}
			sources[id][watchSources[i]] = true
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	stored, err := s.load(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		for _, source := range watchSources {
			if !sources[id][source] {
				continue
			}
			p, ok := stored[id][source]
			if !ok {
				p = DefaultWatchPreference(id, source)
			}
			if p.Allows(event) {
				result = append(result, id)
				break
			}
		}
	}
	return result, nil
}

func (s *WatchService) GetChainPostIDs(ctx context.Context, repository WatcherRepository, headID string) ([]string, error) {
	if repository == nil {
		return nil, errors.New("watcher repository is required")
	}
	return repository.ChainPostIDs(ctx, headID)
}
