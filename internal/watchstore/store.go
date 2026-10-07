// SPDX-License-Identifier: AGPL-3.0-only
// Local persistence adapter written for the Go migration; no upstream storage
// implementation is copied. This is not a PostgreSQL schema migration.
package watchstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"solsynth.local/go-migration/ports/sphere"
)

type document struct {
	Version  int                                 `json:"version"`
	Accounts map[string][]sphere.WatchPreference `json:"accounts"`
}

// Store serializes one process's access and atomically replaces its JSON file.
// Multiple processes must not share the path. Load errors fail closed.
type Store struct {
	mu   sync.Mutex
	path string
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("watch store path is required")
	}
	s := &Store{path: path}
	if _, err := s.read(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) read() (document, error) {
	d := document{Version: 1, Accounts: map[string][]sphere.WatchPreference{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err = json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("invalid watch store: %w", err)
	}
	if d.Version != 1 || d.Accounts == nil {
		return d, errors.New("unsupported watch store format")
	}
	for id, rows := range d.Accounts {
		if id == "" {
			return d, errors.New("empty persisted account ID")
		}
		seen := map[sphere.WatchSource]bool{}
		for i := range rows {
			if !validSource(rows[i].Source) || seen[rows[i].Source] {
				return d, errors.New("invalid or duplicate persisted source")
			}
			seen[rows[i].Source] = true
			rows[i].AccountID = id
		}
		d.Accounts[id] = rows
	}
	return d, nil
}
func (s *Store) LoadPreferences(ctx context.Context, accountIDs []string) ([]sphere.WatchPreference, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]sphere.WatchPreference, 0)
	seen := map[string]bool{}
	for _, id := range accountIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, d.Accounts[id]...)
	}
	return out, nil
}
func (s *Store) SavePreferences(ctx context.Context, accountID string, updates []sphere.WatchPreference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if accountID == "" {
		return errors.New("empty account ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read()
	if err != nil {
		return err
	}
	rows := d.Accounts[accountID]
	seen := map[sphere.WatchSource]bool{}
	for _, update := range updates {
		if !validSource(update.Source) || seen[update.Source] {
			return errors.New("invalid or duplicate preference source")
		}
		seen[update.Source] = true
		if update.AccountID != "" && update.AccountID != accountID {
			return errors.New("cross-account preference update")
		}
		update.AccountID = accountID
		found := false
		for i := range rows {
			if rows[i].Source == update.Source {
				rows[i] = update
				found = true
				break
			}
		}
		if !found {
			rows = append(rows, update)
		}
	}
	d.Accounts[accountID] = rows
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".watch-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validSource(source sphere.WatchSource) bool {
	return source == sphere.WatchBookmark || source == sphere.WatchReaction || source == sphere.WatchReply
}
