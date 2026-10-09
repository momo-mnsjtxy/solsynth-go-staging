// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/passport/notabledays"
)

// A projection may wait after reading an event (for example in a composed media
// or activity adapter). Retain its original observations and compare them with
// current ReadCommitted statements before exposing any assembled response.
// Revalidation takes no additional row locks or external I/O.
type calendarReadSetKey struct{}
type calendarReadSet struct {
	events    map[string]string
	versions  map[string]int64
	snapshots map[string]notabledays.Event
	activity  *calendarActivityRead
}
type calendarActivityRead struct {
	owner, viewer, zone, fingerprint string
	from, to                         time.Time
}

func observeCalendarEvent(ctx context.Context, event notabledays.Event) error {
	reads, ok := ctx.Value(calendarReadSetKey{}).(*calendarReadSet)
	if !ok {
		return nil
	}
	value, err := fingerprint(event)
	if err != nil {
		return err
	}
	if previous, exists := reads.events[event.ID]; exists && previous != value {
		return notabledays.ErrConflict
	}
	reads.events[event.ID] = value
	reads.versions[event.ID] = event.Version
	reads.snapshots[event.ID] = event
	return nil
}

func observeCalendarActivity(ctx context.Context, owner, viewer string, from, to time.Time, zone string, values []notabledays.Overlay) error {
	reads, ok := ctx.Value(calendarReadSetKey{}).(*calendarReadSet)
	if !ok {
		return nil
	}
	value, err := fingerprint(values)
	if err == nil {
		reads.activity = &calendarActivityRead{owner: owner, viewer: viewer, from: from, to: to, zone: zone, fingerprint: value}
	}
	return err
}

func (r *Repository) validateCalendarReads(ctx context.Context, tx *sql.Tx, actor string, c notabledays.Command, reads *calendarReadSet) error {
	ids := make([]string, 0, len(reads.events))
	for id := range reads.events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		event, err := scanEvent(tx.QueryRowContext(ctx, `SELECT `+eventFields+eventBase+` AND e.id=$2::uuid`, actor, id))
		if err != nil {
			return err
		}
		if r.media != nil {
			if err = r.media.Load(ctx, tx, actor, &event); err != nil {
				return err
			}
		}
		value, err := fingerprint(event)
		if err != nil {
			return err
		}
		if value != reads.events[id] {
			return notabledays.ErrConflict
		}
	}
	if r.media != nil && len(ids) != 0 {
		events := make([]notabledays.Event, 0, len(ids))
		for _, id := range ids {
			events = append(events, reads.snapshots[id])
		}
		if err := r.media.Validate(ctx, tx, events); err != nil {
			return err
		}
	}
	if strings.HasPrefix(c.Operation, "calendar.") {
		if _, _, err := resolveAccount(ctx, tx, actor, c.Query.Owner); err != nil {
			return err
		}
	}
	if a := reads.activity; a != nil {
		values, err := r.cfg.Activity.CalendarActivity(ctx, tx, a.owner, a.viewer, a.from, a.to, a.zone)
		if err != nil {
			return err
		}
		value, err := fingerprint(values)
		if err != nil {
			return err
		}
		if value != a.fingerprint {
			return notabledays.ErrConflict
		}
	}
	// One final statement covers the entire event set after all projection
	// callbacks. A later event's media wait cannot retain an earlier event's
	// withdrawn audience or old version in a multi-event response.
	if len(ids) != 0 {
		expected, err := json.Marshal(reads.versions)
		if err != nil {
			return err
		}
		var current int
		err = tx.QueryRowContext(ctx, `SELECT count(*)`+eventBase+` AND EXISTS(SELECT 1 FROM jsonb_each_text($2::jsonb) expected WHERE e.id=expected.key::uuid AND e.version=expected.value::bigint)`, actor, string(expected)).Scan(&current)
		if err != nil {
			return err
		}
		if current != len(ids) {
			return notabledays.ErrNotFound
		}
	}
	return nil
}
