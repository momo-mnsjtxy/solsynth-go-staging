// SPDX-License-Identifier: AGPL-3.0-only
package fortunepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"solsynth.local/go-migration/ports/passport/progression"
	"time"
)

// CalendarOverlay decorates current canonical activity without creating state
// during a read. Only owner-visible check-ins are hydrated, using the same guard.
type CalendarOverlay struct {
	Base    notabledaypg.ActivityOverlay
	Reports *Repository
}

func (a CalendarOverlay) Ready(ctx context.Context, db *sql.DB) error {
	if a.Base == nil || a.Reports == nil || a.Reports.db != db {
		return fortunes.ErrUnavailable
	}
	if e := a.Base.Ready(ctx, db); e != nil {
		return e
	}
	return a.Reports.Ready(ctx)
}
func (a CalendarOverlay) UnavailableMergedComponents() []string {
	if p, ok := a.Base.(interface{ UnavailableMergedComponents() []string }); ok {
		return p.UnavailableMergedComponents()
	}
	return nil
}
func (a CalendarOverlay) CalendarActivity(ctx context.Context, tx *sql.Tx, owner, viewer string, from, to time.Time, zone string) ([]notabledays.Overlay, error) {
	if a.Base == nil || a.Reports == nil {
		return nil, fortunes.ErrUnavailable
	}
	values, e := a.Base.CalendarActivity(ctx, tx, owner, viewer, from, to, zone)
	if e != nil || owner != viewer {
		return values, e
	}
	for i := range values {
		if len(values[i].CheckIn) == 0 || string(values[i].CheckIn) == "null" {
			continue
		}
		var v progression.CheckIn
		if e = json.Unmarshal(values[i].CheckIn, &v); e != nil {
			return nil, e
		}
		if v.AccountID != owner {
			return nil, fortunes.ErrInvalid
		}
		if e = a.Reports.HydrateCheckIn(ctx, tx, &v); e != nil {
			return nil, e
		}
		values[i].CheckIn, e = json.Marshal(v)
		if e != nil {
			return nil, e
		}
	}
	return values, nil
}
