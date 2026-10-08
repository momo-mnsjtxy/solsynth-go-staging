// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"fmt"
	"sort"
	"time"
)

func civilDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
func dayDistance(a, b time.Time) int { return int((civilDate(b).Unix() - civilDate(a).Unix()) / 86400) }
func monthDays(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
func isoDay(t time.Time) int {
	n := int(t.Weekday())
	if n == 0 {
		return 7
	}
	return n
}

// wallInstant uses observed zone offsets rather than time.Date's undocumented
// fold choice. Ambiguous times choose the earlier instant; nonexistent wall
// times are skipped. No host-local timezone participates in recurrence.
func wallInstant(date, clock time.Time, zone *time.Location) (time.Time, bool) {
	h, m, s := clock.Clock()
	naive := time.Date(date.Year(), date.Month(), date.Day(), h, m, s, clock.Nanosecond(), time.UTC)
	offsets := map[int]bool{}
	for hours := -48; hours <= 48; hours += 6 {
		_, offset := naive.Add(time.Duration(hours) * time.Hour).In(zone).Zone()
		offsets[offset] = true
	}
	var result time.Time
	for offset := range offsets {
		candidate := naive.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(zone)
		if local.Year() == date.Year() && local.Month() == date.Month() && local.Day() == date.Day() && local.Hour() == h && local.Minute() == m && local.Second() == s && local.Nanosecond() == clock.Nanosecond() {
			if result.IsZero() || candidate.Before(result) {
				result = candidate
			}
		}
	}
	return result, !result.IsZero()
}
func weekdays(event Event) []int {
	out := []int{}
	r := event.Recurrence
	if r != nil {
		for _, name := range r.DaysOfWeek {
			d, ok := Weekday(name)
			if ok {
				out = append(out, d)
			}
		}
	}
	if len(out) == 0 {
		loc, _ := Zone(event.TimeZone)
		out = []int{isoDay(event.StartTime.In(loc))}
	}
	sort.Ints(out)
	return out
}
func slotDate(event Event, n int) (time.Time, bool) {
	loc, e := Zone(event.TimeZone)
	if e != nil || n < 0 {
		return time.Time{}, false
	}
	anchor := civilDate(event.StartTime.In(loc))
	r := event.Recurrence
	if n == 0 {
		return anchor, true
	}
	if r == nil || r.Frequency == None || r.Interval < 1 {
		return time.Time{}, false
	}
	var date time.Time
	switch r.Frequency {
	case Daily:
		date = anchor.AddDate(0, 0, n*r.Interval)
	case Weekly:
		days := weekdays(event)
		first := []int{}
		for _, d := range days {
			if d > isoDay(anchor) {
				first = append(first, d)
			}
		}
		weekStart := anchor.AddDate(0, 0, 1-isoDay(anchor))
		if n <= len(first) {
			date = weekStart.AddDate(0, 0, first[n-1]-1)
		} else {
			index := n - 1 - len(first)
			week := (index/len(days) + 1) * r.Interval
			date = weekStart.AddDate(0, 0, week*7+days[index%len(days)]-1)
		}
	case Monthly:
		index := anchor.Year()*12 + int(anchor.Month()) - 1 + n*r.Interval
		y, m := index/12, time.Month(index%12+1)
		day := anchor.Day()
		if r.DayOfMonth != nil {
			day = *r.DayOfMonth
		}
		date = time.Date(y, m, min(day, monthDays(y, m)), 0, 0, 0, 0, time.UTC)
	case Yearly:
		y := anchor.Year() + n*r.Interval
		m, day := anchor.Month(), anchor.Day()
		if r.MonthOfYear != nil {
			m = time.Month(*r.MonthOfYear)
		}
		if r.DayOfMonth != nil {
			day = *r.DayOfMonth
		}
		date = time.Date(y, m, min(day, monthDays(y, m)), 0, 0, 0, 0, time.UTC)
	default:
		return time.Time{}, false
	}
	return date, date.Year() >= 1900 && date.Year() <= 2199
}
func estimateSlot(event Event, date time.Time) int {
	loc, e := Zone(event.TimeZone)
	if e != nil || event.Recurrence == nil {
		return 0
	}
	anchor := civilDate(event.StartTime.In(loc))
	r := event.Recurrence
	if !date.After(anchor) {
		return 0
	}
	switch r.Frequency {
	case Daily:
		return max(0, dayDistance(anchor, date)/r.Interval-1)
	case Weekly:
		days := weekdays(event)
		weekStart := anchor.AddDate(0, 0, 1-isoDay(anchor))
		week := max(0, dayDistance(weekStart, date)/7/r.Interval)
		if week == 0 {
			return 0
		}
		first := 0
		for _, d := range days {
			if d > isoDay(anchor) {
				first++
			}
		}
		return max(0, 1+first+(week-1)*len(days)-1)
	case Monthly:
		months := (date.Year()-anchor.Year())*12 + int(date.Month()-anchor.Month())
		return max(0, months/r.Interval-1)
	case Yearly:
		return max(0, (date.Year()-anchor.Year())/r.Interval-1)
	}
	return 0
}
func occurrence(event Event, n int) (Event, bool, error) {
	r := event.Recurrence
	if r != nil && r.Occurrences != nil && n >= *r.Occurrences {
		return Event{}, false, nil
	}
	date, ok := slotDate(event, n)
	if !ok {
		return Event{}, false, nil
	}
	loc, e := Zone(event.TimeZone)
	if e != nil {
		return Event{}, false, e
	}
	var start time.Time
	if n == 0 {
		start = event.StartTime
	} else {
		var valid bool
		start, valid = wallInstant(date, event.StartTime.In(loc), loc)
		if !valid {
			return Event{}, false, nil
		}
	}
	if r != nil && r.EndDate != nil && !start.Before(*r.EndDate) {
		return Event{}, false, nil
	}
	end := start.Add(event.EndTime.Sub(event.StartTime))
	if event.IsAllDay {
		days := dayDistance(event.StartTime.In(loc), event.EndTime.In(loc))
		if days > 0 {
			var valid bool
			end, valid = wallInstant(date.AddDate(0, 0, days), event.EndTime.In(loc), loc)
			if !valid {
				return Event{}, false, nil
			}
		}
	}
	if !end.After(start) {
		return Event{}, false, ErrInvalid
	}
	out := event
	out.StartTime = start.UTC()
	out.EndTime = end.UTC()
	out.Recurrence = nil
	out.OccurrenceKey = fmt.Sprintf("%s@%s", event.ID, start.UTC().Format(time.RFC3339Nano))
	return out, true, nil
}
func Expand(event Event, from, to time.Time) ([]Event, error) {
	if !ValidWindow(from, to) {
		return nil, ErrInvalid
	}
	out := []Event{}
	if event.Recurrence == nil || event.Recurrence.Frequency == None {
		if event.EndTime.After(from) && event.StartTime.Before(to) {
			out = append(out, event)
		}
		return out, nil
	}
	loc, e := Zone(event.TimeZone)
	if e != nil {
		return nil, e
	}
	lookback := int(event.EndTime.Sub(event.StartTime)/(24*time.Hour)) + 2
	first := estimateSlot(event, civilDate(from.In(loc)).AddDate(0, 0, -lookback))
	lastDate := civilDate(to.In(loc)).AddDate(0, 0, 1)
	for n := first; n < 200000; n++ {
		if event.Recurrence.Occurrences != nil && n >= *event.Recurrence.Occurrences {
			break
		}
		date, ok := slotDate(event, n)
		if !ok || date.After(lastDate) {
			break
		}
		v, ok, e := occurrence(event, n)
		if e != nil {
			return nil, e
		}
		if ok && v.EndTime.After(from) && v.StartTime.Before(to) {
			out = append(out, v)
			if len(out) > MaxResults {
				return nil, ErrLimit
			}
		}
	}
	return out, nil
}

// Nearest returns at most the source's three countdown occurrences: the latest
// past occurrence followed by ongoing/future occurrences, without rescanning
// years of daily history or letting an old anchor hide an unbounded recurrence.
func Nearest(event Event, now time.Time) ([]Event, error) {
	if event.Recurrence == nil {
		return []Event{event}, nil
	}
	loc, e := Zone(event.TimeZone)
	if e != nil {
		return nil, e
	}
	lookback := int(event.EndTime.Sub(event.StartTime)/(24*time.Hour)) + 2
	n := max(0, estimateSlot(event, civilDate(now.In(loc)).AddDate(0, 0, -lookback))-8)
	r := event.Recurrence
	if r.Occurrences != nil {
		n = min(n, max(0, *r.Occurrences-8))
	}
	if r.EndDate != nil && r.EndDate.Before(now) {
		n = min(n, max(0, estimateSlot(event, civilDate(r.EndDate.In(loc)))-8))
	}
	out := []Event{}
	var past *Event
	for tries := 0; tries < 2048; tries++ {
		index := n + tries
		if r.Occurrences != nil && index >= *r.Occurrences {
			break
		}
		date, ok := slotDate(event, index)
		if !ok {
			break
		}
		if r.EndDate != nil && date.After(civilDate(r.EndDate.In(loc)).AddDate(0, 0, 1)) {
			break
		}
		v, ok, e := occurrence(event, index)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		if !v.EndTime.After(now) {
			copy := v
			past = &copy
			continue
		}
		if len(out) == 0 && past != nil {
			out = append(out, *past)
		}
		out = append(out, v)
		if len(out) >= 3 {
			return out, nil
		}
	}
	if len(out) == 0 && past != nil {
		return []Event{*past}, nil
	}
	return out, nil
}
