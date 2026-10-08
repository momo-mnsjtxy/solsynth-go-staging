// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"sort"
	"time"
)

func (r *Repository) calendar(ctx context.Context, tx *sql.Tx, actor string, c notabledays.Command, now time.Time) (out notabledays.Result, e error) {
	q := c.Query
	if !notabledays.ValidQuery(q) {
		return out, notabledays.ErrInvalid
	}
	owner, region, e := resolveAccount(ctx, tx, actor, q.Owner)
	if e != nil {
		return out, e
	}
	if q.Region == "" {
		q.Region = region
	}
	if q.TimeZone == "" {
		q.TimeZone = "UTC"
	}
	loc, e := notabledays.Zone(q.TimeZone)
	if e != nil {
		return out, e
	}
	if owner.ID == actor {
		q.Owner = "me"
	} else {
		q.Owner = owner.ID
	}
	switch c.Operation {
	case "calendar.search":
		from, to, e := eventRange(q, now)
		if e != nil {
			return out, e
		}
		q.StartTime = &from
		q.EndTime = &to
		events, e := listEvents(ctx, tx, actor, q)
		if e != nil {
			return out, e
		}
		events, e = expandEvents(events, from, to)
		if e != nil {
			return out, e
		}
		days, e := generatedRange(ctx, tx, q, from, to)
		if e != nil {
			return out, e
		}
		items := []notabledays.SearchItem{}
		for i := range events {
			v := events[i]
			items = append(items, notabledays.SearchItem{Type: 0, StartTime: v.StartTime, EndTime: v.EndTime, UserEvent: &v})
		}
		for i := range days {
			v := days[i]
			items = append(items, notabledays.SearchItem{Type: 1, StartTime: v.Date, EndTime: v.Date, NotableDay: &v})
		}
		if len(items) > notabledays.MaxResults {
			return out, notabledays.ErrLimit
		}
		sort.SliceStable(items, func(i, j int) bool {
			if !items[i].StartTime.Equal(items[j].StartTime) {
				return items[i].StartTime.Before(items[j].StartTime)
			}
			if !items[i].EndTime.Equal(items[j].EndTime) {
				return items[i].EndTime.Before(items[j].EndTime)
			}
			return items[i].Type < items[j].Type
		})
		out.Value, out.Total = page(items, q)
		return out, nil
	case "calendar.countdown":
		events, e := listEvents(ctx, tx, actor, q)
		if e != nil {
			return out, e
		}
		items := []notabledays.Countdown{}
		for _, v := range events {
			nearest, e := notabledays.Nearest(v, now)
			if e != nil {
				return out, e
			}
			for _, event := range nearest {
				id, account := event.ID, event.AccountID
				items = append(items, notabledays.Countdown{EventID: &id, EventType: 0, OccurrenceKey: event.OccurrenceKey, Title: event.Title, Description: event.Description, Location: event.Location, StartTime: event.StartTime, EndTime: event.EndTime, IsAllDay: event.IsAllDay, Meta: event.Meta, AccountID: &account})
			}
			if len(items) > notabledays.MaxResults {
				return out, notabledays.ErrLimit
			}
		}
		if q.IncludeNotable {
			for year := max(1900, now.Year()-1); year <= min(2199, now.Year()+2); year++ {
				days, e := generatedDays(ctx, tx, year, q.Region, q)
				if e != nil {
					return out, e
				}
				for _, d := range days {
					description := d.LocalName
					items = append(items, notabledays.Countdown{EventType: 1, OccurrenceKey: d.OccurrenceKey, Title: d.GlobalName, Description: &description, StartTime: d.Date, EndTime: d.Date.AddDate(0, 0, 1), IsAllDay: true, Meta: d.Meta})
				}
				if len(items) > notabledays.MaxResults {
					return out, notabledays.ErrLimit
				}
			}
		}
		for i := range items {
			delta := items[i].StartTime.Sub(now)
			items[i].DaysRemaining = max(0, int64(delta.Hours()/24))
			items[i].HoursRemaining = max(0, int64(delta.Hours())%24)
			items[i].IsOngoing = !items[i].StartTime.After(now) && items[i].EndTime.After(now)
		}
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if a.IsOngoing != b.IsOngoing {
				return a.IsOngoing
			}
			pastA, pastB := a.StartTime.Before(now), b.StartTime.Before(now)
			if pastA != pastB {
				return !pastA
			}
			if !a.StartTime.Equal(b.StartTime) {
				if pastA {
					return a.StartTime.After(b.StartTime)
				}
				return a.StartTime.Before(b.StartTime)
			}
			return a.Title < b.Title
		})
		out.Value, out.Total = page(items, q)
		return out, nil
	case "calendar.month", "calendar.merged":
		if q.Year == 0 {
			q.Year = now.In(loc).Year()
		}
		if q.Month == 0 {
			q.Month = int(now.In(loc).Month())
		}
		if q.Year < 1900 || q.Year > 2199 || q.Month < 1 || q.Month > 12 {
			return out, notabledays.ErrInvalid
		}
		from := time.Date(q.Year, time.Month(q.Month), 1, 0, 0, 0, 0, loc)
		to := from.AddDate(0, 1, 0)
		q.StartTime = &from
		q.EndTime = &to
		events, e := listEvents(ctx, tx, actor, q)
		if e != nil {
			return out, e
		}
		events, e = expandEvents(events, from.UTC(), to.UTC())
		if e != nil {
			return out, e
		}
		sortEvents(events)
		days := []notabledays.Occurrence{}
		if q.IncludeNotable || c.Operation == "calendar.merged" {
			all, e := generatedDays(ctx, tx, q.Year, q.Region, q)
			if e != nil {
				return out, e
			}
			for _, d := range all {
				if int(d.Date.UTC().Month()) == q.Month && d.Date.UTC().Year() == q.Year {
					days = append(days, d)
				}
			}
		}
		overlay := map[string]notabledays.Overlay{}
		unavailable := []string{}
		if r.cfg.Activity != nil {
			values, e := r.cfg.Activity.CalendarActivity(ctx, tx, owner.ID, actor, from.UTC(), to.UTC(), q.TimeZone)
			if e != nil {
				return out, e
			}
			for _, value := range values {
				overlay[value.Date] = value
			}
		} else {
			unavailable = []string{"statuses", "check_in_result"}
		}
		calendar := []notabledays.DailyCalendar{}
		for date := from; date.Before(to); date = date.AddDate(0, 0, 1) {
			next := date.AddDate(0, 0, 1)
			entry := notabledays.DailyCalendar{Date: date.UTC(), CheckInResult: json.RawMessage(`null`), Statuses: []json.RawMessage{}, UserEvents: []notabledays.Event{}, NotableDays: []notabledays.Occurrence{}, UnavailableComponents: unavailable}
			for _, event := range events {
				if event.StartTime.Before(next) && event.EndTime.After(date) {
					entry.UserEvents = append(entry.UserEvents, event)
				}
			}
			for _, day := range days {
				if day.Date.UTC().Format("2006-01-02") == date.Format("2006-01-02") {
					entry.NotableDays = append(entry.NotableDays, day)
				}
			}
			if data, ok := overlay[date.Format("2006-01-02")]; ok {
				if len(data.CheckIn) > 0 {
					entry.CheckInResult = data.CheckIn
				}
				if data.Statuses != nil {
					entry.Statuses = data.Statuses
				}
			}
			calendar = append(calendar, entry)
		}
		if c.Operation == "calendar.month" {
			out.Value = calendar
			return out, nil
		}
		merged := notabledays.MergedCalendar{Date: from.UTC(), CheckInResult: json.RawMessage(`null`), Statuses: []json.RawMessage{}, UserEvents: events, NotableDays: days, MergedEvents: []notabledays.MergedEvent{}, UnavailableComponents: append([]string{}, unavailable...)}
		if len(calendar) > 0 {
			merged.CheckInResult = calendar[0].CheckInResult
		}
		for _, entry := range calendar {
			merged.Statuses = append(merged.Statuses, entry.Statuses...)
			if data, ok := overlay[entry.Date.In(loc).Format("2006-01-02")]; ok {
				merged.MergedEvents = append(merged.MergedEvents, data.Merged...)
			}
		}
		for _, v := range events {
			id := v.ID
			merged.MergedEvents = append(merged.MergedEvents, notabledays.MergedEvent{ID: &id, Type: 0, Title: v.Title, Description: v.Description, Location: v.Location, StartTime: v.StartTime, EndTime: v.EndTime, IsAllDay: v.IsAllDay, Meta: v.Meta, OccurrenceKey: v.OccurrenceKey})
		}
		for _, v := range days {
			description := v.LocalName
			merged.MergedEvents = append(merged.MergedEvents, notabledays.MergedEvent{Type: 1, Title: v.GlobalName, Description: &description, StartTime: v.Date, EndTime: v.Date.AddDate(0, 0, 1), IsAllDay: true, Meta: v.Meta, OccurrenceKey: v.OccurrenceKey})
		}
		if adapter, ok := r.cfg.Activity.(interface{ UnavailableMergedComponents() []string }); ok {
			merged.UnavailableComponents = append(merged.UnavailableComponents, adapter.UnavailableMergedComponents()...)
		}
		sort.SliceStable(merged.MergedEvents, func(i, j int) bool { return merged.MergedEvents[i].StartTime.Before(merged.MergedEvents[j].StartTime) })
		out.Value = merged
		return out, nil
	}
	return out, notabledays.ErrUnsupported
}
