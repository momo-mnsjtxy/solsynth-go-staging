// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/friends"
	"solsynth.local/go-migration/ports/passport/presence"
	"sort"
	"strings"
)

var _ friends.Repository = (*Repository)(nil)

// Overview keeps the complete projection in one transaction. Canonical account
// locks serialize profile, friendship, preferences, source-session and activity
// changes. The initial discovery grants nothing; all candidates are rechecked
// after sorted locks and again after the last artwork wait.
func (r *Repository) Overview(ctx context.Context, actor string, f friends.Filter) (out []friends.Item, total int, e error) {
	out = []friends.Item{}
	if e = friends.Validate(f); e != nil {
		return
	}
	if r == nil || r.db == nil || r.guard == nil {
		return nil, 0, friends.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, 0, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer tx.Rollback()
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor); e != nil {
		return nil, 0, e
	}
	ids, e := socialpg.FriendsInTransaction(ctx, tx, actor, 1001)
	if e != nil {
		return nil, 0, e
	}
	if len(ids) > 1000 {
		return nil, 0, friends.ErrUnavailable
	}
	locked := map[string]bool{}
	locks := append(append([]string{}, ids...), actor)
	sort.Strings(locks)
	for _, id := range locks {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, id).Scan(&found); e != nil {
			return nil, 0, e
		}
		locked[id] = true
	}
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	current, e := socialpg.FriendsInTransaction(ctx, tx, actor, 1001)
	if e != nil {
		return nil, 0, e
	}
	ids = ids[:0]
	for _, id := range current {
		if locked[id] {
			ids = append(ids, id)
		}
	}
	prepared, e := r.prepareFriendActivities(ctx, tx, actor, ids)
	if e != nil {
		return nil, 0, e
	}
	// No domain locks follow this point. Refresh all owner/activity/artwork gates
	// after the complete wait set before composing any observable fallback/count.
	for i := range prepared {
		if e = artwork(ctx, tx, prepared[i].AccountID, &prepared[i].Content, false); e != nil {
			return nil, 0, e
		}
	}
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	visibleActivities := map[string][]activities.Activity{}
	for _, v := range prepared {
		allow, friend, x := audience(ctx, tx, actor, v.AccountID)
		if errors.Is(x, activities.ErrAccountNotFound) {
			continue
		}
		if x != nil {
			return nil, 0, x
		}
		if !allow || !friend {
			continue
		}
		eligible, x := r.eligible(ctx, tx, v)
		if x != nil {
			return nil, 0, x
		}
		if eligible {
			visibleActivities[v.AccountID] = append(visibleActivities[v.AccountID], v.Activity)
		}
	}
	now, e := clock(ctx, tx)
	if e != nil {
		return nil, 0, e
	}
	current, e = socialpg.FriendsInTransaction(ctx, tx, actor, 1001)
	if e != nil {
		return nil, 0, e
	}
	accepted := map[string]bool{}
	for _, id := range current {
		accepted[id] = locked[id]
	}
	type candidate struct {
		item   friends.Item
		recent bool
	}
	candidates := []candidate{}
	for _, id := range ids {
		if !accepted[id] {
			continue
		}
		status, recent, x := presencepg.FriendStatusInTransaction(ctx, tx, actor, id, now)
		if errors.Is(x, presence.ErrNotFound) {
			continue
		}
		if x != nil {
			return nil, 0, x
		}
		// An invisible owner is absent even in the recent/offline fallback.
		if status.AccountID == "" {
			continue
		}
		account, x := accountpg.New(r.db).AccountInTransaction(ctx, tx, actor, id)
		if errors.Is(x, accounts.ErrNotFound) {
			continue
		}
		if x != nil {
			return nil, 0, x
		}
		if account.Profile == nil {
			account.Profile = &accounts.Profile{Links: []accounts.Link{}}
		}
		live := []activities.Activity{}
		for _, v := range visibleActivities[id] {
			if now.Before(v.LeaseExpiresAt) && v.EndedAt == nil {
				live = append(live, v)
			}
		}
		item := friends.Item{Account: account, Status: status, Activities: live}
		candidates = append(candidates, candidate{item, recent})
	}
	// Reader token expiry is never replaced by a fresh proof or wall-clock seed.
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	// Status/profile hydration can take time even with the domain locks held.
	// Fence leases after that work and the original-proof check, then decide the
	// fallback and visible count. An expired activity cannot suppress recent peers.
	now, e = clock(ctx, tx)
	if e != nil {
		return nil, 0, e
	}
	anyActive := false
	for i := range candidates {
		item := &candidates[i].item
		live := item.Activities[:0]
		for _, v := range item.Activities {
			if v.EndedAt == nil && now.Before(v.LeaseExpiresAt) {
				live = append(live, v)
			}
		}
		item.Activities = live
		anyActive = anyActive || item.Status.IsOnline || len(live) > 0
	}
	for _, v := range candidates {
		if v.item.Status.IsOnline || len(v.item.Activities) > 0 || v.recent && (f.IncludeOffline || !anyActive) {
			out = append(out, v.item)
		}
	}
	total = len(out)
	if f.Paged {
		start := f.Offset
		if start > total {
			start = total
		}
		end := start + f.Take
		if end > total {
			end = total
		}
		out = out[start:end]
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return
}

func (r *Repository) prepareFriendActivities(ctx context.Context, tx *sql.Tx, viewer string, ids []string) ([]row, error) {
	allowed := []string{}
	for _, id := range ids {
		allow, friend, e := audience(ctx, tx, viewer, id)
		if errors.Is(e, activities.ErrAccountNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if allow && friend {
			allowed = append(allowed, id)
		}
	}
	if len(allowed) == 0 {
		return []row{}, nil
	}
	rs, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE account_id=ANY($1::uuid[]) AND deleted_at IS NULL AND ended_at IS NULL AND lease_expires_at>clock_timestamp() AND visibility IN(1,2) ORDER BY account_id,COALESCE(started_at,created_at) DESC,id LIMIT 1001`, allowed)
	if e != nil {
		return nil, e
	}
	all := []row{}
	for rs.Next() {
		v, x := scan(rs)
		if x != nil {
			rs.Close()
			return nil, x
		}
		all = append(all, v)
	}
	e = rs.Err()
	rs.Close()
	if e != nil {
		return nil, e
	}
	if len(all) > 1000 {
		return nil, friends.ErrUnavailable
	}
	// Acquire the full association set globally before any file lock. Shared or
	// foreign references cannot invert lock order across multiple owners.
	hashes := []string{}
	for _, v := range all {
		for _, p := range []*string{v.LargeImage, v.SmallImage} {
			if p != nil && strings.HasPrefix(*p, "sha256:") {
				hashes = append(hashes, *p)
			}
		}
	}
	sort.Strings(hashes)
	for i, hash := range hashes {
		if i > 0 && hashes[i-1] == hash {
			continue
		}
		if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('presence-artwork:' || $1::text,0))`, hash); e != nil {
			return nil, e
		}
	}
	for i := range all {
		if e = artwork(ctx, tx, all[i].AccountID, &all[i].Content, false); e != nil {
			return nil, e
		}
	}
	return all, nil
}
