// SPDX-License-Identifier: AGPL-3.0-only
// Publication watcher resolution reuses canonical watcher/preference behavior
// inside the caller's publication transaction. It performs no delivery or commit.
package timelinepg

import (
	"context"
	"database/sql"

	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

type publicationWatches struct {
	repo *Repository
	tx   *sql.Tx
}

func (r *Repository) PublicationWatcherAccounts(ctx context.Context, tx *sql.Tx, ids []string, event sphere.WatchEvent) ([]string, error) {
	if tx == nil {
		return nil, timeline.ErrInvalid
	}
	w := publicationWatches{r, tx}
	return (&sphere.WatchService{Repository: w}).ResolveWatcherAccountIDs(ctx, w, ids, event)
}

func (w publicationWatches) WatchAccounts(ctx context.Context, ids []string) (sphere.WatchAccounts, error) {
	out := sphere.WatchAccounts{}
	for len(ids) > 0 {
		n := min(len(ids), 100)
		v, e := w.repo.watchAccountsInTransaction(ctx, w.tx, ids[:n])
		if e != nil {
			return out, e
		}
		out.Bookmarkers = append(out.Bookmarkers, v.Bookmarkers...)
		out.Reactors = append(out.Reactors, v.Reactors...)
		out.Repliers = append(out.Repliers, v.Repliers...)
		ids = ids[n:]
	}
	return out, nil
}
func (w publicationWatches) LoadPreferences(ctx context.Context, ids []string) ([]sphere.WatchPreference, error) {
	var out []sphere.WatchPreference
	for len(ids) > 0 {
		n := min(len(ids), 1000)
		v, e := loadWatch(ctx, w.tx, ids[:n])
		if e != nil {
			return nil, e
		}
		out = append(out, v...)
		ids = ids[n:]
	}
	return out, nil
}
func (publicationWatches) SavePreferences(context.Context, string, []sphere.WatchPreference) error {
	return timeline.ErrInvalid
}
func (publicationWatches) ChainPostIDs(context.Context, string) ([]string, error) {
	return nil, timeline.ErrInvalid
}
