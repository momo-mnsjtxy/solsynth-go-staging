// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func Priority(meta json.RawMessage) int {
	var m map[string]json.RawMessage
	if json.Unmarshal(meta, &m) != nil {
		return 3
	}
	raw := bytes.TrimSpace(m["priority"])
	if len(raw) == 0 || raw[0] == '"' {
		return 3
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 3
	}
	i, e := strconv.ParseInt(string(n), 10, 32)
	if e != nil {
		return 3
	}
	return int(i)
}
func lunar(meta json.RawMessage) bool {
	var m map[string]any
	if json.Unmarshal(meta, &m) != nil {
		return false
	}
	v, _ := m["calendar"].(string)
	return strings.EqualFold(v, "lunar")
}
func recurrenceDate(day Day, year int) (time.Time, bool, error) {
	if day.RecurrencePattern == nil {
		return time.Time{}, false, ErrInvalid
	}
	m, d, ok := Pattern(*day.RecurrencePattern)
	if !ok {
		return time.Time{}, false, ErrInvalid
	}
	if lunar(day.Meta) {
		return ResolveLunar(year, m, d)
	}
	if d > monthDays(year, time.Month(m)) {
		return time.Time{}, false, nil
	}
	return time.Date(year, time.Month(m), d, 0, 0, 0, 0, time.UTC), true, nil
}
func OccurrenceKey(region string, date time.Time, identity string) string {
	return strings.ToLower(strings.TrimSpace(region)) + "|" + date.UTC().Format("2006-01-02") + "|" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(identity)), "|", "-")
}
func notableOccurrence(day Day, date time.Time) Occurrence {
	local := day.Name
	if day.LocalName != nil {
		local = *day.LocalName
	}
	identity := day.Name
	if day.LocalizableKey != nil && strings.TrimSpace(*day.LocalizableKey) != "" {
		identity = *day.LocalizableKey
	}
	region := day.Region
	out := Occurrence{Date: date.UTC(), LocalName: local, GlobalName: day.Name, LocalizableKey: day.LocalizableKey, CountryCode: &region, Description: day.Description, Meta: day.Meta, Holidays: []string{}, Tags: append([]int{}, day.Tags...), OccurrenceKey: OccurrenceKey(day.Region, date, identity), SourceID: &day.ID}
	for _, tag := range day.Tags {
		if tag == Holiday {
			out.Holidays = []string{"Public"}
			break
		}
	}
	return out
}
func GenerateDay(day Day, year int) ([]Occurrence, error) {
	if year < 1900 || year > 2199 {
		return nil, ErrInvalid
	}
	out := []Occurrence{}
	if !day.IsRecurring {
		if day.StartDate.UTC().Year() == year {
			out = append(out, notableOccurrence(day, day.StartDate))
		}
		return out, nil
	}
	date, ok, e := recurrenceDate(day, year)
	if e != nil {
		return nil, e
	}
	if !ok {
		return out, nil
	}
	if !day.IsPeriod {
		return []Occurrence{notableOccurrence(day, date)}, nil
	}
	reference, ok, e := recurrenceDate(day, day.StartDate.UTC().Year())
	if e != nil {
		return nil, e
	}
	if !ok {
		reference = civilDate(day.StartDate.UTC())
	}
	startOffset := dayDistance(reference, day.StartDate.UTC())
	endOffset := dayDistance(reference, day.EndDate.UTC())
	if endOffset <= startOffset {
		endOffset = startOffset + 1
	}
	if endOffset-startOffset > 366 {
		return nil, ErrLimit
	}
	for n := startOffset; n < endOffset; n++ {
		out = append(out, notableOccurrence(day, date.AddDate(0, 0, n)))
	}
	return out, nil
}

// OccurrencesInYear includes the portion of an annual period spilling in from
// an adjacent recurrence year. Occurrence keys refer to their actual dates.
func OccurrencesInYear(day Day, year int) ([]Occurrence, error) {
	if year < 1900 || year > 2199 {
		return nil, ErrInvalid
	}
	if !day.IsRecurring || !day.IsPeriod {
		return GenerateDay(day, year)
	}
	if lunar(day.Meta) && (year < 1901 || year > 2101) {
		return nil, ErrUnsupported
	}
	out := []Occurrence{}
	seen := map[string]bool{}
	for anchor := max(1900, year-1); anchor <= min(2199, year+1); anchor++ {
		if lunar(day.Meta) && (anchor < 1901 || anchor > 2101) {
			continue
		}
		values, e := GenerateDay(day, anchor)
		if e != nil {
			return nil, e
		}
		for _, v := range values {
			if v.Date.Year() == year && !seen[v.OccurrenceKey] {
				seen[v.OccurrenceKey] = true
				out = append(out, v)
			}
		}
	}
	SortOccurrences(out)
	return out, nil
}
func GlobalDays(year int, region string) []Occurrence {
	out := []Occurrence{}
	for _, v := range []struct {
		month, day       int
		name, local, key string
		tag              int
	}{{1, 1, "New Year's Day", "New Year's Day", "NewYear", Holiday}, {12, 25, "Christmas", "Christmas", "Christmas", Holiday}, {3, 16, fmt.Sprintf("Solar Network %dth anniversary", year-2024), fmt.Sprintf("Solar Network %d 周年", year-2024), "Anniversary", Anniversary}} {
		date := time.Date(year, time.Month(v.month), v.day, 0, 0, 0, 0, time.UTC)
		key := v.key
		item := Occurrence{Date: date, LocalName: v.local, GlobalName: v.name, LocalizableKey: &key, Tags: []int{v.tag}, Holidays: []string{}, OccurrenceKey: OccurrenceKey(region, date, key)}
		if v.tag == Holiday {
			item.Holidays = []string{"Public"}
		}
		out = append(out, item)
	}
	return out
}
func SortOccurrences(v []Occurrence) {
	sort.Slice(v, func(i, j int) bool {
		if !v[i].Date.Equal(v[j].Date) {
			return v[i].Date.Before(v[j].Date)
		}
		if v[i].GlobalName != v[j].GlobalName {
			return v[i].GlobalName < v[j].GlobalName
		}
		return v[i].OccurrenceKey < v[j].OccurrenceKey
	})
}
func MatchOccurrence(v Occurrence, q Query) bool {
	if q.Tag != nil {
		found := false
		for _, tag := range v.Tags {
			if tag == *q.Tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if q.Layer != nil && Priority(v.Meta) > *q.Layer {
		return false
	}
	if q.Text != "" {
		needle := strings.ToLower(strings.TrimSpace(q.Text))
		text := v.LocalName + " " + v.GlobalName
		if v.Description != nil {
			text += " " + *v.Description
		}
		if v.LocalizableKey != nil {
			text += " " + *v.LocalizableKey
		}
		if !strings.Contains(strings.ToLower(text), needle) {
			return false
		}
	}
	return true
}
