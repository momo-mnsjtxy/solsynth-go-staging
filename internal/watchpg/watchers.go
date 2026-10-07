// SPDX-License-Identifier: AGPL-3.0-only
// Compatibility-candidate reader derived from licensed PostWatchService.cs and
// its own repository's EF model snapshot. See WATCHER-COMPATIBILITY.md.
package watchpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"solsynth.local/go-migration/ports/sphere"
)

var ErrInvalidPostID = errors.New("post ID must be a canonical lowercase UUID")
var ErrUnverifiedWatcherPolicy = errors.New("watcher queries disabled: upstream global-filter policy is unverified")
var _ sphere.WatcherRepository = (*Repository)(nil)

// NewSoftDeleteOnlyCompatibilityTestCandidate explicitly opts into the fixed,
// unverified live-row query policy for synthetic compatibility tests only.
// It must not be connected to production notification or recipient exposure.
// Ordinary New repositories keep both watcher methods disabled, fail closed.
func NewSoftDeleteOnlyCompatibilityTestCandidate(db *sql.DB) *Repository {
	return &Repository{db: db, softDeleteOnlyWatcherCandidate: true}
}

// watchPostArgs validates every input before any SQL is issued. UUID placeholders
// are generated locally and UUID values are always bound as parameters.
func watchPostArgs(ids []string) (string, []any, error) {
	seen := make(map[string]bool, len(ids))
	args := make([]any, 0, len(ids))
	marks := make([]string, 0, len(ids))
	for _, id := range ids {
		if !validAccount(id) {
			return "", nil, ErrInvalidPostID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		args = append(args, id)
		marks = append(marks, fmt.Sprintf("$%d::uuid", len(args)))
	}
	return strings.Join(marks, ","), args, nil
}

func readWatchIDs(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// WatchAccounts reads candidate watcher account sets, not authorized recipients.
// It intentionally does not add post visibility, target-post existence, draft,
// realm, federation-localness, or shadowban filters absent from the source query.
// The caller remains responsible for deciding whether/how an event is delivered.
//
// Compatibility assumption: deleted_at IS NULL is applied to each queried entity
// (including the joined publisher), corresponding to the licensed AppDatabase's
// call to ApplySoftDeleteFilters. That shared hook's implementation has not been
// inspected; exact global-filter/runtime parity is NOT established.
func (r *Repository) WatchAccounts(ctx context.Context, postIDs []string) (sphere.WatchAccounts, error) {
	result := sphere.WatchAccounts{Bookmarkers: []string{}, Reactors: []string{}, Repliers: []string{}}
	if r == nil || !r.softDeleteOnlyWatcherCandidate {
		return result, ErrUnverifiedWatcherPolicy
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	marks, args, err := watchPostArgs(postIDs)
	if err != nil {
		return result, err
	}
	if len(args) == 0 {
		return result, nil
	}
	if err = r.ready(ctx); err != nil {
		return result, err
	}
	result.Bookmarkers, err = readWatchIDs(ctx, r.db, `SELECT DISTINCT account_id::text FROM post_bookmarks WHERE deleted_at IS NULL AND post_id IN (`+marks+`) ORDER BY 1`, args...)
	if err != nil {
		return sphere.WatchAccounts{}, err
	}
	result.Reactors, err = readWatchIDs(ctx, r.db, `SELECT DISTINCT account_id::text FROM post_reactions WHERE deleted_at IS NULL AND account_id IS NOT NULL AND post_id IN (`+marks+`) ORDER BY 1`, args...)
	if err != nil {
		return sphere.WatchAccounts{}, err
	}
	result.Repliers, err = readWatchIDs(ctx, r.db, `SELECT DISTINCT pub.account_id::text FROM posts AS p JOIN publishers AS pub ON pub.id=p.publisher_id WHERE p.deleted_at IS NULL AND pub.deleted_at IS NULL AND pub.account_id IS NOT NULL AND p.replied_post_id IN (`+marks+`) ORDER BY 1`, args...)
	if err != nil {
		return sphere.WatchAccounts{}, err
	}
	return result, nil
}

// ChainPostIDs returns the live head (if present) and live direct chained posts.
// It is not a recursive traversal and does not require the head to be present or
// live. As above, the explicit soft-delete predicate is a documented assumption.
// Neither this method nor WatchAccounts promises upstream result ordering.
func (r *Repository) ChainPostIDs(ctx context.Context, headID string) ([]string, error) {
	if r == nil || !r.softDeleteOnlyWatcherCandidate {
		return nil, ErrUnverifiedWatcherPolicy
	}
	if !validAccount(headID) {
		return nil, ErrInvalidPostID
	}
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	return readWatchIDs(ctx, r.db, `SELECT id::text FROM posts WHERE deleted_at IS NULL AND (id=$1::uuid OR chained_post_id=$1::uuid) ORDER BY id`, headID)
}
