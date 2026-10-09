// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"encoding/json"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

var regionPattern = regexp.MustCompile(`^[A-Za-z]{2}(?:-[A-Za-z0-9]{1,5})?$`)

func Text(s string, min, max int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(strings.TrimSpace(s)) >= min && utf8.RuneCountInString(s) <= max
}
func optional(s *string, max int) bool { return s == nil || Text(*s, 0, max) }
func ValidInstant(v time.Time) bool    { return !v.IsZero() && v.Year() >= 1900 && v.Year() <= 2199 }

// ValidWindow allows adjacent UTC boundary dates needed to project the first
// and last supported local calendar months in any named time zone.
func ValidWindow(from, to time.Time) bool {
	return !from.Before(time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)) && !to.After(time.Date(2200, 1, 3, 0, 0, 0, 0, time.UTC)) && to.After(from) && to.Sub(from) <= 732*24*time.Hour
}
func ValidKey(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func metadata(b json.RawMessage) bool {
	if len(b) == 0 || string(b) == "null" {
		return true
	}
	var m map[string]json.RawMessage
	return len(b) <= 16384 && json.Unmarshal(b, &m) == nil && m != nil
}
func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) > 16 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if !Text(tag, 1, 64) {
			return nil, ErrInvalid
		}
		seen[tag] = true
	}
	out := []string{}
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}
func ValidQuery(q Query) bool {
	return q.Take >= 1 && q.Take <= 100 && q.Offset >= 0 && q.Offset <= 100000 && len(q.Text) <= 256 && (q.AccountID == "" || authority.ValidID(q.AccountID)) && (q.Tag == nil || *q.Tag >= 0 && *q.Tag <= 4) && (q.Layer == nil || *q.Layer >= 0 && *q.Layer <= 1000)
}
func Zone(name string) (*time.Location, error) {
	if name == "" {
		name = "UTC"
	}
	if len(name) > 128 || strings.ContainsAny(name, "\\\x00") || name == "Local" {
		return nil, ErrInvalid
	}
	loc, e := time.LoadLocation(name)
	if e != nil {
		return nil, ErrInvalid
	}
	return loc, nil
}

var weekdayNames = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

func Weekday(name string) (int, bool) {
	for i, s := range weekdayNames {
		if strings.EqualFold(name, s) {
			return i + 1, true
		}
	}
	return 0, false
}
func ValidateEvent(v *EventBody) error {
	if !Text(v.Title, 1, 256) || !optional(v.Description, 4096) || !optional(v.Location, 1024) || !ValidInstant(v.StartTime) || !ValidInstant(v.EndTime) || !v.EndTime.After(v.StartTime) || v.EndTime.Sub(v.StartTime) > 366*24*time.Hour || !metadata(v.Meta) {
		return ErrInvalid
	}
	if v.Visibility != Private && v.Visibility != Friends && v.Visibility != Public {
		return ErrInvalid
	}
	if v.TimeZone == "" {
		v.TimeZone = "UTC"
	}
	if _, e := Zone(v.TimeZone); e != nil {
		return e
	}
	tags, e := NormalizeTags(v.Tags)
	if e != nil {
		return e
	}
	v.Tags = tags
	v.StartTime = v.StartTime.UTC()
	v.EndTime = v.EndTime.UTC()
	r := v.Recurrence
	if r == nil {
		return nil
	}
	if r.Frequency == None {
		v.Recurrence = nil
		return nil
	}
	if r.Frequency < Daily || r.Frequency > Yearly || r.Interval < 1 || r.Interval > 366 || r.Occurrences != nil && (*r.Occurrences < 1 || *r.Occurrences > 10000) || r.EndDate != nil && (!ValidInstant(*r.EndDate) || !r.EndDate.After(v.StartTime)) {
		return ErrInvalid
	}
	if r.DayOfMonth != nil && (*r.DayOfMonth < 1 || *r.DayOfMonth > 31) || r.MonthOfYear != nil && (*r.MonthOfYear < 1 || *r.MonthOfYear > 12) {
		return ErrInvalid
	}
	if len(r.DaysOfWeek) > 7 {
		return ErrInvalid
	}
	if len(r.DaysOfWeek) > 0 && r.Frequency != Weekly || r.DayOfMonth != nil && r.Frequency != Monthly && r.Frequency != Yearly || r.MonthOfYear != nil && r.Frequency != Yearly {
		return ErrUnsupported
	}
	seen := map[int]bool{}
	days := []int{}
	for _, name := range r.DaysOfWeek {
		d, ok := Weekday(name)
		if !ok || seen[d] {
			return ErrInvalid
		}
		seen[d] = true
		days = append(days, d)
	}
	sort.Ints(days)
	r.DaysOfWeek = []string{}
	for _, d := range days {
		r.DaysOfWeek = append(r.DaysOfWeek, weekdayNames[d-1])
	}
	return nil
}
func Pattern(s string) (int, int, bool) {
	p := strings.Split(s, "-")
	if len(p) != 2 {
		return 0, 0, false
	}
	m, e := strconv.Atoi(p[0])
	if e != nil {
		return 0, 0, false
	}
	d, e := strconv.Atoi(p[1])
	return m, d, e == nil && m >= 1 && m <= 12 && d >= 1 && d <= 31
}
func ValidateDay(v *DayInput) error {
	if !Text(v.Name, 1, 256) || !optional(v.Description, 4096) || !optional(v.LocalName, 256) || !optional(v.LocalizableKey, 256) || !ValidInstant(v.StartDate) || !ValidInstant(v.EndDate) || !v.EndDate.After(v.StartDate) || v.EndDate.Sub(v.StartDate) > 366*24*time.Hour || !regionPattern.MatchString(v.Region) || !metadata(v.Meta) || len(v.Tags) > 5 || len(v.HolidayDays) > 366 || v.DisplayOrder != nil && (*v.DisplayOrder < 0 || *v.DisplayOrder > 100000) {
		return ErrInvalid
	}
	seen := map[int]bool{}
	for _, t := range v.Tags {
		if t < 0 || t > 4 || seen[t] {
			return ErrInvalid
		}
		seen[t] = true
	}
	if v.Tags == nil {
		v.Tags = []int{}
	}
	sort.Ints(v.Tags)
	if v.HolidayDays == nil {
		v.HolidayDays = []string{}
	}
	for _, d := range v.HolidayDays {
		if _, e := time.Parse("2006-01-02", d); e != nil {
			return ErrInvalid
		}
	}
	if v.IsRecurring {
		if v.RecurrencePattern == nil {
			return ErrInvalid
		}
		_, _, ok := Pattern(*v.RecurrencePattern)
		if !ok {
			return ErrInvalid
		}
	} else if v.RecurrencePattern != nil && strings.TrimSpace(*v.RecurrencePattern) != "" {
		return ErrInvalid
	}
	v.Region = strings.ToUpper(v.Region)
	v.StartDate = v.StartDate.UTC()
	v.EndDate = v.EndDate.UTC()
	var meta map[string]any
	if len(v.Meta) > 0 && string(v.Meta) != "null" {
		if e := json.Unmarshal(v.Meta, &meta); e != nil {
			return ErrInvalid
		}
		if cal, ok := meta["calendar"]; ok {
			value, ok := cal.(string)
			if !ok || !strings.EqualFold(value, "lunar") && !strings.EqualFold(value, "solar") && !strings.EqualFold(value, "gregorian") {
				return ErrUnsupported
			}
		}
	}
	return nil
}
