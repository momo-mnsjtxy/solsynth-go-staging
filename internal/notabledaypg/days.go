// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"sort"
	"strings"
	"time"
)

func scanDay(row interface{ Scan(...any) error }) (d notabledays.Day, e error) {
	var b []byte
	var start, end time.Time
	e = row.Scan(&d.ID, &b, &start, &end, &d.CreatedAt, &d.UpdatedAt, &d.DeletedAt)
	if e != nil {
		return d, mapped(e)
	}
	e = json.Unmarshal(b, &d.DayInput)
	d.StartDate = start.UTC()
	d.EndDate = end.UTC()
	return
}

const dayFields = `id::text,definition,start_date,end_date,created_at,updated_at,deleted_at`

func loadDay(ctx context.Context, tx *sql.Tx, id string, lock bool) (notabledays.Day, error) {
	if !authority.ValidID(id) {
		return notabledays.Day{}, notabledays.ErrNotFound
	}
	query := `SELECT ` + dayFields + ` FROM owned_notable_days WHERE id=$1::uuid AND deleted_at IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanDay(tx.QueryRowContext(ctx, query, id))
}
func rawDays(ctx context.Context, tx *sql.Tx, region string) ([]notabledays.Day, error) {
	rows, e := tx.QueryContext(ctx, `SELECT `+dayFields+` FROM owned_notable_days WHERE lower(region)=lower($1::text) AND deleted_at IS NULL ORDER BY COALESCE(display_order,999),start_date,id LIMIT 1001`, region)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []notabledays.Day{}
	for rows.Next() {
		d, e := scanDay(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
		if len(out) > notabledays.MaxCandidates {
			return nil, notabledays.ErrLimit
		}
	}
	return out, rows.Err()
}
func (r *Repository) days(ctx context.Context, tx *sql.Tx, actor string, c notabledays.Command, now time.Time) (out notabledays.Result, e error) {
	switch c.Operation {
	case "day.list":
		q := c.Query
		if !notabledays.ValidQuery(q) {
			return out, notabledays.ErrInvalid
		}
		if q.Year == 0 {
			q.Year = now.Year()
		}
		if q.Year < 1900 || q.Year > 2199 {
			return out, notabledays.ErrInvalid
		}
		if q.Region == "" {
			q.Region = "CN"
		}
		v, e := rawDays(ctx, tx, q.Region)
		if e != nil {
			return out, e
		}
		start := time.Date(q.Year, 1, 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(1, 0, 0)
		filtered := []notabledays.Day{}
		for _, d := range v {
			if !d.IsRecurring && (!d.StartDate.Before(end) || d.EndDate.Before(start)) {
				continue
			}
			if q.Layer != nil && notabledays.Priority(d.Meta) > *q.Layer {
				continue
			}
			if q.Tag != nil {
				found := false
				for _, tag := range d.Tags {
					if tag == *q.Tag {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			filtered = append(filtered, d)
		}
		out.Value, out.Total = page(filtered, q)
		return out, nil
	case "day.get":
		out.Value, e = loadDay(ctx, tx, c.ID, false)
		return out, e
	case "day.generated":
		parts := strings.SplitN(c.OccurrenceKey, "|", 3)
		if len(parts) != 3 || len(c.OccurrenceKey) > 512 {
			return out, notabledays.ErrNotFound
		}
		date, e := time.Parse("2006-01-02", parts[1])
		if e != nil {
			return out, notabledays.ErrNotFound
		}
		q := c.Query
		q.Region = parts[0]
		q.Year = date.Year()
		v, e := generatedDays(ctx, tx, q.Year, q.Region, q)
		if e != nil {
			return out, e
		}
		for _, d := range v {
			if d.OccurrenceKey == c.OccurrenceKey {
				out.Value = d
				return out, nil
			}
		}
		return out, notabledays.ErrNotFound
	case "day.create", "day.update":
		if c.Day == nil {
			return out, notabledays.ErrInvalid
		}
		v := *c.Day
		if e = notabledays.ValidateDay(&v); e != nil {
			return out, e
		}
		var d notabledays.Day
		if c.Operation == "day.update" {
			d, e = loadDay(ctx, tx, c.ID, true)
			if e != nil {
				return out, e
			}
		} else {
			if _, e = tx.ExecContext(ctx, `LOCK TABLE owned_notable_days IN SHARE ROW EXCLUSIVE MODE`); e != nil {
				return out, e
			}
			var count int
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_notable_days WHERE deleted_at IS NULL`).Scan(&count); e != nil {
				return out, e
			}
			if count >= 1000 {
				return out, notabledays.ErrLimit
			}
			d.ID, e = id()
			if e != nil {
				return out, e
			}
			d.CreatedAt = now
		}
		d.DayInput = v
		d.UpdatedAt = now
		b, e := json.Marshal(v)
		if e != nil {
			return out, e
		}
		if c.Operation == "day.create" {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_notable_days(id,definition,region,start_date,end_date,is_recurring,display_order,created_at,updated_at)VALUES($1::uuid,$2::jsonb,$3::text,$4::timestamptz,$5::timestamptz,$6::boolean,$7::integer,$8::timestamptz,$8::timestamptz)`, d.ID, string(b), d.Region, d.StartDate, d.EndDate, d.IsRecurring, d.DisplayOrder, now)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE owned_notable_days SET definition=$2::jsonb,region=$3::text,start_date=$4::timestamptz,end_date=$5::timestamptz,is_recurring=$6::boolean,display_order=$7::integer,updated_at=$8::timestamptz WHERE id=$1::uuid`, d.ID, string(b), d.Region, d.StartDate, d.EndDate, d.IsRecurring, d.DisplayOrder, now)
		}
		out.Value = d
		out.Changed = e == nil
		out.ResourceID = d.ID
		return out, e
	case "day.delete":
		d, e := loadDay(ctx, tx, c.ID, true)
		if e != nil {
			return out, e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_notable_days SET deleted_at=$2::timestamptz,updated_at=$2::timestamptz WHERE id=$1::uuid`, d.ID, now)
		out.Changed = e == nil
		out.ResourceID = d.ID
		return out, e
	}
	return out, notabledays.ErrUnsupported
}
func generatedDays(ctx context.Context, tx *sql.Tx, year int, region string, q notabledays.Query) ([]notabledays.Occurrence, error) {
	if year < 1900 || year > 2199 || len(region) > 8 {
		return nil, notabledays.ErrInvalid
	}
	records, e := rawDays(ctx, tx, region)
	if e != nil {
		return nil, e
	}
	out := []notabledays.Occurrence{}
	keys := map[string]int{}
	for _, d := range records {
		values, e := notabledays.OccurrencesInYear(d, year)
		if e != nil {
			return nil, e
		}
		for _, v := range values {
			if !notabledays.MatchOccurrence(v, q) {
				continue
			}
			if _, ok := keys[v.OccurrenceKey]; ok {
				return nil, notabledays.ErrConflict
			}
			keys[v.OccurrenceKey] = len(out)
			out = append(out, v)
			if len(out) > notabledays.MaxResults {
				return nil, notabledays.ErrLimit
			}
		}
	}
	for _, v := range notabledays.GlobalDays(year, region) {
		if !notabledays.MatchOccurrence(v, q) {
			continue
		}
		duplicate := false
		for _, d := range out {
			if d.Date.Equal(v.Date) && d.GlobalName == v.GlobalName {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if _, ok := keys[v.OccurrenceKey]; ok {
			return nil, notabledays.ErrConflict
		}
		keys[v.OccurrenceKey] = len(out)
		out = append(out, v)
	}
	notabledays.SortOccurrences(out)
	return out, nil
}
func generatedRange(ctx context.Context, tx *sql.Tx, q notabledays.Query, from, to time.Time) ([]notabledays.Occurrence, error) {
	if to.Year()-from.Year() > 4 {
		return nil, notabledays.ErrLimit
	}
	out := []notabledays.Occurrence{}
	for year := from.UTC().Year(); year <= to.Add(-time.Nanosecond).UTC().Year(); year++ {
		v, e := generatedDays(ctx, tx, year, q.Region, q)
		if e != nil {
			return nil, e
		}
		for _, d := range v {
			if !d.Date.Before(from) && d.Date.Before(to) {
				out = append(out, d)
				if len(out) > notabledays.MaxResults {
					return nil, notabledays.ErrLimit
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}
