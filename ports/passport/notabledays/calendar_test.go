// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func instant(s string) time.Time {
	v, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return v
}
func number(n int) *int { return &n }
func recurring(start, end, zone string, frequency int, count *int) Event {
	return Event{ID: "10000000-0000-4000-8000-000000000001", EventBody: EventBody{Title: "Synthetic calendar", StartTime: instant(start), EndTime: instant(end), TimeZone: zone, Tags: []string{}, Recurrence: &Recurrence{Frequency: frequency, Interval: 1, Occurrences: count}}}
}
func TestChineseLunisolarFactsMatchOfficialOracle(t *testing.T) {
	b, e := os.ReadFile("oracle/results.json")
	if e != nil {
		t.Fatal(e)
	}
	var expected struct {
		Runtime string `json:"runtime"`
		First   int    `json:"first_lunar_year"`
		Last    int    `json:"last_resolution_year"`
		Count   int    `json:"checked_cases"`
		Digest  string `json:"resolution_sha256"`
	}
	if e = json.Unmarshal(b, &expected); e != nil {
		t.Fatal(e)
	}
	h := sha256.New()
	count := 0
	for year := expected.First; year <= expected.Last; year++ {
		for month := 1; month <= 12; month++ {
			for day := 1; day <= 31; day++ {
				v, ok, e := ResolveLunar(year, month, day)
				if e != nil {
					t.Fatal(year, month, day, e)
				}
				result := "null"
				if ok {
					result = v.Format("2006-01-02")
				}
				fmt.Fprintf(h, "%d|%d|%d|%s\n", year, month, day, result)
				count++
			}
		}
	}
	if count != 74772 || count != expected.Count || hex.EncodeToString(h.Sum(nil)) != expected.Digest {
		t.Fatal(count, hex.EncodeToString(h.Sum(nil)), expected.Digest)
	}
	if expected.Runtime != ".NET 10.0.12" {
		t.Fatal(expected.Runtime)
	}
	if _, _, e = ResolveLunar(1900, 1, 1); e != ErrUnsupported {
		t.Fatal(e)
	}
	last, ok, e := ResolveLunar(2101, 12, 30)
	if e != nil || !ok || last.After(instant("2101-01-28T00:00:00Z")) {
		t.Fatal(last, ok, e)
	}
}
func TestCivilRecurrenceDSTGapFoldAndAllDay(t *testing.T) {
	v := recurring("2026-03-07T14:00:00Z", "2026-03-07T15:00:00Z", "America/New_York", Daily, number(3))
	events, e := Expand(v, instant("2026-03-07T00:00:00Z"), instant("2026-03-11T00:00:00Z"))
	if e != nil || len(events) != 3 {
		t.Fatal(events, e)
	}
	want := []string{"2026-03-07T14:00:00Z", "2026-03-08T13:00:00Z", "2026-03-09T13:00:00Z"}
	for i, event := range events {
		if event.StartTime.Format(time.RFC3339) != want[i] {
			t.Fatal(i, event.StartTime)
		}
	}
	v = recurring("2026-03-07T07:30:00Z", "2026-03-07T08:30:00Z", "America/New_York", Daily, number(3))
	events, e = Expand(v, instant("2026-03-07T00:00:00Z"), instant("2026-03-11T00:00:00Z"))
	if e != nil || len(events) != 2 || events[1].StartTime.Day() != 9 {
		t.Fatal("nonexistent wall time was shifted", events, e)
	}
	v = recurring("2026-10-31T05:30:00Z", "2026-10-31T06:30:00Z", "America/New_York", Daily, number(3))
	events, e = Expand(v, instant("2026-10-31T00:00:00Z"), instant("2026-11-04T00:00:00Z"))
	if e != nil || len(events) != 3 || events[1].StartTime.Format(time.RFC3339) != "2026-11-01T05:30:00Z" {
		t.Fatal("fold not deterministic", events, e)
	}
	v = recurring("2026-03-07T05:00:00Z", "2026-03-08T05:00:00Z", "America/New_York", Daily, number(3))
	v.IsAllDay = true
	events, e = Expand(v, instant("2026-03-07T00:00:00Z"), instant("2026-03-11T00:00:00Z"))
	if e != nil || len(events) != 3 || events[1].EndTime.Sub(events[1].StartTime) != 23*time.Hour {
		t.Fatal("all-day interval ignored DST", events, e)
	}
}
func TestMonthlyAnchorWeeklyIntervalAndLimits(t *testing.T) {
	v := recurring("2026-01-31T09:00:00Z", "2026-01-31T10:00:00Z", "UTC", Monthly, number(3))
	events, e := Expand(v, instant("2026-01-01T00:00:00Z"), instant("2026-04-01T00:00:00Z"))
	if e != nil || len(events) != 3 || events[1].StartTime.Day() != 28 || events[2].StartTime.Day() != 31 || events[2].StartTime.Hour() != 9 {
		t.Fatal(events, e)
	}
	v = recurring("2026-11-02T09:00:00Z", "2026-11-02T10:00:00Z", "UTC", Weekly, number(5))
	v.Recurrence.Interval = 2
	v.Recurrence.DaysOfWeek = []string{"Monday", "Wednesday"}
	events, e = Expand(v, instant("2026-11-01T00:00:00Z"), instant("2026-12-01T00:00:00Z"))
	want := []int{2, 4, 16, 18, 30}
	if e != nil || len(events) != len(want) {
		t.Fatal(events, e)
	}
	for i, v := range events {
		if v.StartTime.Day() != want[i] {
			t.Fatal(i, v.StartTime)
		}
	}
	v.Recurrence.Interval = 0
	if e = ValidateEvent(&v.EventBody); e == nil {
		t.Fatal("zero interval accepted")
	}
	v.TimeZone = "Local"
	if _, e = Zone(v.TimeZone); e == nil {
		t.Fatal("host timezone accepted")
	}
}
func TestLongLivedAndCompletedCountdowns(t *testing.T) {
	v := recurring("2000-01-01T09:00:00Z", "2000-01-01T10:00:00Z", "UTC", Daily, nil)
	events, e := Expand(v, instant("2026-10-08T00:00:00Z"), instant("2026-10-10T00:00:00Z"))
	if e != nil || len(events) != 2 {
		t.Fatal("old anchor disappeared", events, e)
	}
	near, e := Nearest(v, instant("2026-10-08T08:00:00Z"))
	if e != nil || len(near) != 3 || near[0].StartTime.Day() != 7 || near[1].StartTime.Day() != 8 {
		t.Fatal(near, e)
	}
	v.Recurrence.Occurrences = number(2)
	near, e = Nearest(v, instant("2026-10-08T08:00:00Z"))
	if e != nil || len(near) != 1 || near[0].StartTime.Format("2006-01-02") != "2000-01-02" {
		t.Fatal(near, e)
	}
}
func TestNotableLunarPeriodPriorityAndOccurrenceKeys(t *testing.T) {
	name, pattern := "lunar-new-year", "01-01"
	d := Day{ID: "10000000-0000-4000-8000-000000000001", DayInput: DayInput{Name: "Spring", LocalizableKey: &name, StartDate: instant("2026-02-16T00:00:00Z"), EndDate: instant("2026-02-20T00:00:00Z"), IsRecurring: true, IsPeriod: true, RecurrencePattern: &pattern, Region: "CN", Tags: []int{Holiday}, Meta: json.RawMessage(`{"calendar":"lunar","priority":1}`)}}
	v, e := GenerateDay(d, 2027)
	if e != nil || len(v) != 4 || v[0].Date.Format("2006-01-02") != "2027-02-05" || v[3].Date.Format("2006-01-02") != "2027-02-08" {
		t.Fatal(v, e)
	}
	if !strings.HasPrefix(v[0].OccurrenceKey, "cn|2027-02-05|") || Priority(json.RawMessage(`{"priority":"1"}`)) != 3 || Priority(d.Meta) != 1 {
		t.Fatal(v)
	}
	if key := OccurrenceKey(" CN ", instant("2026-01-01T00:00:00Z"), "New | Day"); key != "cn|2026-01-01|new - day" {
		t.Fatal(key)
	}
}

func TestNotablePeriodCrossYearLookup(t *testing.T) {
	pattern := "12-31"
	d := Day{ID: "10000000-0000-4000-8000-000000000001", DayInput: DayInput{Name: "Year boundary", StartDate: instant("2026-12-30T00:00:00Z"), EndDate: instant("2027-01-03T00:00:00Z"), IsRecurring: true, IsPeriod: true, RecurrencePattern: &pattern, Region: "CN"}}
	values, e := OccurrencesInYear(d, 2027)
	want := []string{"2027-01-01", "2027-01-02", "2027-12-30", "2027-12-31"}
	if e != nil || len(values) != len(want) {
		t.Fatal(values, e)
	}
	for i, value := range values {
		if value.Date.Format("2006-01-02") != want[i] {
			t.Fatal(i, value)
		}
	}
}

func TestSupportedLocalCalendarWindowBounds(t *testing.T) {
	for _, item := range []struct{ zone, start, from, to string }{
		{"Asia/Tokyo", "1900-01-01T00:00:00Z", "1899-12-31T15:00:00Z", "1900-01-31T15:00:00Z"},
		{"America/New_York", "2199-12-30T14:00:00Z", "2199-12-01T05:00:00Z", "2200-01-01T05:00:00Z"},
	} {
		v := recurring(item.start, instant(item.start).Add(time.Hour).Format(time.RFC3339), item.zone, Daily, number(1))
		values, e := Expand(v, instant(item.from), instant(item.to))
		if e != nil || len(values) != 1 {
			t.Fatal(item, values, e)
		}
	}
}
