// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	_ "embed"
	"encoding/json"
	"time"
)

// lunarFacts contains calendar facts generated independently from the public
// .NET 10.0.12 ChineseLunisolarCalendar API. No runtime source table was copied.
//
//go:embed lunar_years.json
var lunarFacts []byte

type lunarYear struct {
	Year     int    `json:"year"`
	NewYear  string `json:"new_year"`
	LeapSlot int    `json:"leap_slot"`
	Months   []int  `json:"months"`
}

var lunarYears = func() map[int]lunarYear {
	var years []lunarYear
	if e := json.Unmarshal(lunarFacts, &years); e != nil {
		panic("invalid embedded calendar facts")
	}
	out := map[int]lunarYear{}
	for _, year := range years {
		out[year.Year] = year
	}
	return out
}()

// ResolveLunar resolves a regular (not leap-month) lunar month/day into the
// requested Gregorian year, matching the authored service's previous-year
// fallback and lunar month 12 day 30 -> 29 rule.
func ResolveLunar(year, month, day int) (time.Time, bool, error) {
	if year < 1901 || year > 2101 {
		return time.Time{}, false, ErrUnsupported
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false, ErrInvalid
	}
	for _, candidate := range []int{year, year - 1} {
		record, ok := lunarYears[candidate]
		if !ok {
			continue
		}
		slot := month
		if record.LeapSlot > 0 && month >= record.LeapSlot {
			slot++
		}
		if slot < 1 || slot > len(record.Months) {
			continue
		}
		d := day
		if d > record.Months[slot-1] {
			if month == 12 && day == 30 {
				d = 29
			} else {
				continue
			}
		}
		start, e := time.Parse("2006-01-02", record.NewYear)
		if e != nil {
			return time.Time{}, false, ErrUnavailable
		}
		offset := d - 1
		for _, days := range record.Months[:slot-1] {
			offset += days
		}
		date := start.AddDate(0, 0, offset)
		if date.Year() == year {
			return date, true, nil
		}
	}
	return time.Time{}, false, nil
}
