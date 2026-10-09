// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/friends"
)

func (r *Repository) FriendActivitiesInTransaction(ctx context.Context, tx *sql.Tx, viewer string, s friendeventpg.Source) (json.RawMessage, bool, error) {
	if r == nil || r.db == nil || tx == nil {
		return nil, false, activities.ErrUnavailable
	}
	current, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid`, s.Activity, s.Owner))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	if current.revision != s.Revision || current.Visibility != activities.Public && current.Visibility != activities.Friends || current.session != s.Session || current.epoch != s.Epoch || current.version != s.Version {
		return nil, false, nil
	}
	rows, e := r.prepareFriendActivities(ctx, tx, viewer, []string{s.Owner})
	if errors.Is(e, friends.ErrUnavailable) {
		// A deterministic snapshot bound must not poison the shared local queue.
		// Suppress the oversized snapshot; a later bounded mutation can emit anew.
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	for i := range rows {
		if e = artwork(ctx, tx, s.Owner, &rows[i].Content, false); e != nil {
			return nil, false, e
		}
	}
	allow, friend, e := audience(ctx, tx, viewer, s.Owner)
	if errors.Is(e, activities.ErrAccountNotFound) {
		return nil, false, nil
	}
	if e != nil || !allow || !friend {
		return nil, false, e
	}
	live := []activities.Activity{}
	for _, v := range rows {
		ok, x := r.eligible(ctx, tx, v)
		if x != nil {
			return nil, false, x
		}
		if ok {
			live = append(live, v.Activity)
		}
	}
	now, e := clock(ctx, tx)
	if e != nil {
		return nil, false, e
	}
	filtered := live[:0]
	for _, v := range live {
		if now.Before(v.LeaseExpiresAt) {
			filtered = append(filtered, v)
		}
	}
	data, e := json.Marshal(map[string]any{"account_id": s.Owner, "activities": filtered, "timestamp": s.At})
	if e != nil {
		return nil, false, e
	}
	if len(data) > notifications.MaxPayload {
		return nil, false, nil
	}
	return data, true, nil
}
