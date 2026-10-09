// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"sort"
	"time"
)

// CanonicalActivityOverlay reads the existing presence and progression schemas;
// it owns no status/check-in state. Optional wire values must be provided by the
// host when its merged activity enum contract is verified. Daily fields work
// without them. Current presence privacy is rechecked; check-ins stay owner-only.
type CanonicalActivityOverlay struct{ CheckInType, StatusType *int }

func (CanonicalActivityOverlay) Ready(ctx context.Context, db *sql.DB) error {
	_, e := db.ExecContext(ctx, `SELECT account_id,status_visibility FROM owned_presence_preferences LIMIT 0; SELECT id,account_id,type,label,created_at FROM owned_presence_statuses LIMIT 0; SELECT id,account_id,level,reward_experience,created_at FROM owned_progression_checkins LIMIT 0`)
	return e
}
func (a CanonicalActivityOverlay) UnavailableMergedComponents() []string {
	out := []string{}
	if a.CheckInType == nil {
		out = append(out, "merged_check_ins")
	}
	if a.StatusType == nil {
		out = append(out, "merged_statuses")
	}
	return out
}
func (a CanonicalActivityOverlay) CalendarActivity(ctx context.Context, tx *sql.Tx, owner, viewer string, from, to time.Time, zone string) ([]notabledays.Overlay, error) {
	loc, e := notabledays.Zone(zone)
	if e != nil {
		return nil, e
	}
	blocked, friend, e := relationship(ctx, tx, owner, viewer)
	if e != nil {
		return nil, e
	}
	if blocked {
		return nil, notabledays.ErrNotFound
	}
	values := map[string]*notabledays.Overlay{}
	entry := func(at time.Time) *notabledays.Overlay {
		key := at.In(loc).Format("2006-01-02")
		if values[key] == nil {
			values[key] = &notabledays.Overlay{Date: key, CheckIn: json.RawMessage(`null`), Statuses: []json.RawMessage{}, Merged: []notabledays.MergedEvent{}}
		}
		return values[key]
	}
	var visibility string
	e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT status_visibility FROM owned_presence_preferences WHERE account_id=$1::uuid),'public')`, owner).Scan(&visibility)
	if e != nil {
		return nil, e
	}
	if viewer == owner || visibility == "public" || visibility == "friends" && friend {
		rows, e := tx.QueryContext(ctx, `SELECT s.id::text,s.attitude,s.type,s.label,s.symbol,s.cleared_at,s.created_at,s.updated_at FROM owned_presence_statuses s JOIN owned_authority_accounts a ON a.id=s.account_id WHERE s.account_id=$1::uuid AND `+activeCalendarOwner+` AND s.created_at>=$2::timestamptz AND s.created_at<$3::timestamptz ORDER BY s.created_at,s.id LIMIT 501`, owner, from, to)
		if e != nil {
			return nil, e
		}
		count := 0
		for rows.Next() {
			count++
			if count > 500 {
				rows.Close()
				return nil, notabledays.ErrLimit
			}
			var id string
			var attitude, kind int
			var label, symbol *string
			var cleared *time.Time
			var created, updated time.Time
			if e = rows.Scan(&id, &attitude, &kind, &label, &symbol, &cleared, &created, &updated); e != nil {
				rows.Close()
				return nil, e
			}
			if viewer != owner && kind == 3 {
				kind = 0
			}
			data := map[string]any{"id": id, "account_id": owner, "attitude": attitude, "type": kind, "label": label, "symbol": symbol, "cleared_at": cleared, "created_at": created, "updated_at": updated}
			raw, e := json.Marshal(data)
			if e != nil {
				rows.Close()
				return nil, e
			}
			day := entry(created)
			day.Statuses = append(day.Statuses, raw)
			title := "Status Update"
			if label != nil {
				title = *label
			}
			end := created.Add(24 * time.Hour)
			if cleared != nil {
				end = *cleared
			}
			if end.Before(created) {
				end = created
			}
			day.Activities = append(day.Activities, notabledays.ActivityEvent{ID: id, Kind: "status", Title: title, StartTime: created, EndTime: end})
			if a.StatusType != nil {
				day.Merged = append(day.Merged, notabledays.MergedEvent{ID: &id, Type: *a.StatusType, Title: title, StartTime: created, EndTime: end})
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	if viewer == owner {
		rows, e := tx.QueryContext(ctx, `SELECT c.id::text,c.level,c.reward_experience,c.created_at FROM owned_progression_checkins c JOIN owned_authority_accounts a ON a.id=c.account_id WHERE c.account_id=$1::uuid AND `+activeCalendarOwner+` AND c.created_at>=$2::timestamptz AND c.created_at<$3::timestamptz ORDER BY c.created_at,c.id LIMIT 367`, owner, from, to)
		if e != nil {
			return nil, e
		}
		count := 0
		for rows.Next() {
			count++
			if count > 366 {
				rows.Close()
				return nil, notabledays.ErrLimit
			}
			var id string
			var level int
			var xp int64
			var created time.Time
			if e = rows.Scan(&id, &level, &xp, &created); e != nil {
				rows.Close()
				return nil, e
			}
			raw, e := json.Marshal(map[string]any{"id": id, "account_id": owner, "level": level, "reward_experience": xp, "reward_points": nil, "tips": []any{}, "fortune_report": nil, "backdated_from": nil, "created_at": created, "updated_at": created, "deleted_at": nil})
			if e != nil {
				rows.Close()
				return nil, e
			}
			day := entry(created)
			day.CheckIn = raw
			day.Activities = append(day.Activities, notabledays.ActivityEvent{ID: id, Kind: "check_in", Title: fmt.Sprintf("Check-in: %d", level), StartTime: created, EndTime: created, IsAllDay: true})
			if a.CheckInType != nil {
				day.Merged = append(day.Merged, notabledays.MergedEvent{ID: &id, Type: *a.CheckInType, Title: fmt.Sprintf("Check-in: %d", level), StartTime: created, EndTime: created, IsAllDay: true})
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []notabledays.Overlay{}
	for _, key := range keys {
		out = append(out, *values[key])
	}
	return out, nil
}
