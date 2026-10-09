// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"strings"
	"time"
)

const eventFields = `e.id::text,e.account_id::text,e.definition,e.start_time,e.end_time,e.visibility,e.version,e.created_at,e.updated_at,e.deleted_at,a.name,a.nick`
const friendSQL = `(EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE f.account_id=e.account_id AND f.related_id=NULLIF($1::text,'')::uuid AND f.status>=100)AND EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE f.account_id=NULLIF($1::text,'')::uuid AND f.related_id=e.account_id AND f.status>=100))`
const blockedSQL = `EXISTS(SELECT 1 FROM owned_social_effective_relationships b WHERE b.status=-100 AND((b.account_id=e.account_id AND b.related_id=NULLIF($1::text,'')::uuid)OR(b.related_id=e.account_id AND b.account_id=NULLIF($1::text,'')::uuid)))`
const eventBase = ` FROM owned_calendar_events e JOIN owned_authority_accounts a ON a.id=e.account_id WHERE e.deleted_at IS NULL AND ` + activeCalendarOwner + ` AND NOT ` + blockedSQL + ` AND(e.account_id=NULLIF($1::text,'')::uuid OR e.visibility=200 OR(e.visibility=100 AND ` + friendSQL + `))`

func scanEvent(row interface{ Scan(...any) error }) (v notabledays.Event, e error) {
	var b []byte
	var start, end time.Time
	var visibility int
	e = row.Scan(&v.ID, &v.AccountID, &b, &start, &end, &visibility, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt, &v.Account.Name, &v.Account.Nick)
	if e != nil {
		return v, mapped(e)
	}
	if e = json.Unmarshal(b, &v.EventBody); e != nil {
		return v, e
	}
	v.StartTime = start.UTC()
	v.EndTime = end.UTC()
	v.Visibility = visibility
	v.Account.ID = v.AccountID
	return v, nil
}
func (r *Repository) loadEvent(ctx context.Context, tx *sql.Tx, actor, id string, write bool) (notabledays.Event, error) {
	if !authority.ValidID(id) {
		return notabledays.Event{}, notabledays.ErrNotFound
	}
	query := `SELECT ` + eventFields + eventBase + ` AND e.id=$2::uuid`
	if write {
		query += ` AND e.account_id=NULLIF($1::text,'')::uuid FOR UPDATE OF e`
	}
	v, e := scanEvent(tx.QueryRowContext(ctx, query, actor, id))
	if e == nil && r.media != nil && !write {
		e = r.media.Load(ctx, tx, actor, &v)
	}
	if e == nil && !write {
		e = observeCalendarEvent(ctx, v)
	}
	return v, e
}
func applyEvent(v notabledays.EventBody, p notabledays.EventPatch) (notabledays.EventBody, error) {
	if p.Title != nil {
		v.Title = strings.TrimSpace(*p.Title)
	}
	normalize := func(s *string) *string {
		if s == nil {
			return nil
		}
		t := strings.TrimSpace(*s)
		if t == "" {
			return nil
		}
		return &t
	}
	if p.Description != nil {
		v.Description = normalize(p.Description)
	}
	if p.Location != nil {
		v.Location = normalize(p.Location)
	}
	if p.StartTime != nil {
		v.StartTime = *p.StartTime
	}
	if p.EndTime != nil {
		v.EndTime = *p.EndTime
	}
	if p.IsAllDay != nil {
		v.IsAllDay = *p.IsAllDay
	}
	if p.Visibility != nil {
		v.Visibility = *p.Visibility
	}
	if p.Tags != nil {
		v.Tags = append([]string{}, (*p.Tags)...)
	}
	if len(p.Meta) > 0 {
		v.Meta = append(json.RawMessage{}, p.Meta...)
	}
	if p.TimeZone != nil {
		v.TimeZone = *p.TimeZone
	}
	if p.RecurrencePresent || p.Recurrence != nil {
		v.Recurrence = p.Recurrence
	}
	if v.Recurrence != nil {
		r := *v.Recurrence
		r.DaysOfWeek = append([]string{}, r.DaysOfWeek...)
		v.Recurrence = &r
	}
	if e := notabledays.ValidateEvent(&v); e != nil {
		return notabledays.EventBody{}, e
	}
	return v, nil
}
func (r *Repository) events(ctx context.Context, tx *sql.Tx, actor string, c notabledays.Command, now time.Time) (out notabledays.Result, e error) {
	switch c.Operation {
	case "event.create":
		if c.Patch == nil {
			return out, notabledays.ErrInvalid
		}
		v, e := applyEvent(notabledays.EventBody{TimeZone: "UTC", Visibility: notabledays.Private, Tags: []string{}}, *c.Patch)
		if e != nil {
			return out, e
		}
		selection, e := normalizeMediaPatch(*c.Patch, r.media != nil)
		if e != nil {
			return out, e
		}
		hash, e := fingerprint(struct {
			notabledays.EventBody
			IconID       *string `json:"icon_id,omitempty"`
			BackgroundID *string `json:"background_id,omitempty"`
		}{v, selection.IconID, selection.BackgroundID})
		if e != nil {
			return out, e
		}
		if c.IdempotencyKey != "" {
			var priorID string
			var prior sql.NullString
			e = tx.QueryRowContext(ctx, `SELECT id::text,request_hash FROM owned_calendar_events WHERE account_id=$1::uuid AND request_key=$2::text`, actor, c.IdempotencyKey).Scan(&priorID, &prior)
			if e == nil {
				if !prior.Valid || prior.String != hash {
					return out, notabledays.ErrConflict
				}
				out.Value, e = r.loadEvent(ctx, tx, actor, priorID, false)
				return out, e
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return out, e
			}
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_calendar_events WHERE account_id=$1::uuid AND deleted_at IS NULL`, actor).Scan(&count); e != nil {
			return out, e
		}
		if count >= 1000 {
			return out, notabledays.ErrLimit
		}
		eventID, e := id()
		if e != nil {
			return out, e
		}
		b, e := json.Marshal(v)
		if e != nil {
			return out, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_calendar_events(id,account_id,definition,start_time,end_time,visibility,is_recurring,created_at,updated_at,request_key,request_hash)VALUES($1::uuid,$2::uuid,$3::jsonb,$4::timestamptz,$5::timestamptz,$6::integer,$7::boolean,$8::timestamptz,$8::timestamptz,$9::text,$10::text)`, eventID, actor, string(b), v.StartTime, v.EndTime, v.Visibility, v.Recurrence != nil, now, nullable(c.IdempotencyKey), hash)
		if e != nil {
			return out, e
		}
		if r.media != nil {
			if e = r.media.Apply(ctx, tx, actor, eventID, selection); e != nil {
				return out, e
			}
		}
		out.Value, e = r.loadEvent(ctx, tx, actor, eventID, false)
		out.Changed = e == nil
		out.ResourceID = eventID
		return out, e
	case "event.update", "event.delete":
		v, e := r.loadEvent(ctx, tx, actor, c.ID, true)
		if e != nil {
			return out, e
		}
		if c.Operation == "event.delete" {
			if r.media != nil {
				if e = r.media.Remove(ctx, tx, v.ID); e != nil {
					return out, e
				}
			}
			_, e = tx.ExecContext(ctx, `UPDATE owned_calendar_events SET deleted_at=$2::timestamptz,updated_at=$2::timestamptz,version=version+1 WHERE id=$1::uuid`, v.ID, now)
			out.Changed = e == nil
			out.ResourceID = v.ID
			return out, e
		}
		if c.Patch == nil {
			return out, notabledays.ErrInvalid
		}
		selection, e := normalizeMediaPatch(*c.Patch, r.media != nil)
		if e != nil {
			return out, e
		}
		body, e := applyEvent(v.EventBody, *c.Patch)
		if e != nil {
			return out, e
		}
		b, e := json.Marshal(body)
		if e != nil {
			return out, e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_calendar_events SET definition=$2::jsonb,start_time=$3::timestamptz,end_time=$4::timestamptz,visibility=$5::integer,is_recurring=$6::boolean,updated_at=$7::timestamptz,version=version+1 WHERE id=$1::uuid`, v.ID, string(b), body.StartTime, body.EndTime, body.Visibility, body.Recurrence != nil, now)
		if e != nil {
			return out, e
		}
		if r.media != nil {
			if e = r.media.Apply(ctx, tx, actor, v.ID, selection); e != nil {
				return out, e
			}
		}
		out.Value, e = r.loadEvent(ctx, tx, actor, v.ID, false)
		out.Changed = e == nil
		out.ResourceID = v.ID
		return out, e
	case "event.get":
		v, e := r.loadEvent(ctx, tx, actor, c.ID, false)
		if e != nil {
			return out, e
		}
		if c.Query.Owner != "" && c.Query.Owner != "me" && c.Query.Owner != "unknown" {
			owner, _, e := resolveAccount(ctx, tx, actor, c.Query.Owner)
			if e != nil {
				return out, e
			}
			if owner.ID != v.AccountID {
				return out, notabledays.ErrNotFound
			}
		}
		out.Value = v
		return out, nil
	case "event.list":
		q := c.Query
		if !notabledays.ValidQuery(q) {
			return out, notabledays.ErrInvalid
		}
		v, e := r.listEvents(ctx, tx, actor, q)
		if e != nil {
			return out, e
		}
		if q.StartTime != nil || q.EndTime != nil {
			from, to, e := eventRange(q, now)
			if e != nil {
				return out, e
			}
			v, e = expandEvents(v, from, to)
			if e != nil {
				return out, e
			}
		}
		sortEvents(v)
		out.Value, out.Total = page(v, q)
		return out, nil
	case "event.tags":
		q := notabledays.Query{Owner: "me", AccountID: actor, Take: 100}
		events, e := r.listEvents(ctx, tx, actor, q)
		if e != nil {
			return out, e
		}
		all := []string{}
		seen := map[string]bool{}
		for _, v := range events {
			for _, tag := range v.Tags {
				if !seen[tag] {
					seen[tag] = true
					all = append(all, tag)
				}
			}
		}
		sortStrings(all)
		out.Value = all
		return out, nil
	}
	return out, notabledays.ErrUnsupported
}
func (r *Repository) listEvents(ctx context.Context, tx *sql.Tx, actor string, q notabledays.Query) ([]notabledays.Event, error) {
	accessible := q.Owner == "" || q.Owner == "me"
	owner := q.AccountID
	if owner != "" && !authority.ValidID(owner) {
		return nil, notabledays.ErrInvalid
	}
	if !accessible {
		a, _, e := resolveAccount(ctx, tx, actor, q.Owner)
		if e != nil {
			return nil, e
		}
		owner = a.ID
	}
	tags, e := notabledays.NormalizeTags(q.Tags)
	if e != nil {
		return nil, e
	}
	tagJSON, e := json.Marshal(tags)
	if e != nil {
		return nil, e
	}
	query := `SELECT ` + eventFields + eventBase + ` AND($2::text='' OR e.account_id=NULLIF($2::text,'')::uuid) AND(NOT $3::boolean OR e.account_id=NULLIF($1::text,'')::uuid OR ` + friendSQL + ` OR EXISTS(SELECT 1 FROM owned_calendar_subscriptions s WHERE s.subscriber_id=NULLIF($1::text,'')::uuid AND s.target_account_id=e.account_id)) AND($4::text='' OR position(lower($4::text) in lower(COALESCE(e.definition->>'title','')||' '||COALESCE(e.definition->>'description','')||' '||COALESCE(e.definition->>'location','')))>0 OR e.definition->'tags' ? lower($4::text)) AND(e.definition->'tags') @> $5::jsonb AND($6::timestamptz IS NULL OR e.is_recurring OR e.end_time>$6::timestamptz) AND($7::timestamptz IS NULL OR e.is_recurring OR e.start_time<$7::timestamptz) ORDER BY e.start_time,e.id LIMIT 1001`
	rows, e := tx.QueryContext(ctx, query, actor, owner, accessible, strings.TrimSpace(q.Text), string(tagJSON), q.StartTime, q.EndTime)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []notabledays.Event{}
	for rows.Next() {
		v, e := scanEvent(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
		if len(out) > notabledays.MaxCandidates {
			return nil, notabledays.ErrLimit
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if r.media != nil {
		for i := range out {
			if e = r.media.Load(ctx, tx, actor, &out[i]); e != nil {
				return nil, e
			}
		}
	}
	for _, v := range out {
		if e = observeCalendarEvent(ctx, v); e != nil {
			return nil, e
		}
	}
	return out, nil
}
func eventRange(q notabledays.Query, now time.Time) (time.Time, time.Time, error) {
	var from, to time.Time
	switch {
	case q.StartTime != nil && q.EndTime != nil:
		from, to = *q.StartTime, *q.EndTime
	case q.StartTime != nil:
		from = *q.StartTime
		to = from.AddDate(1, 0, 0)
	case q.EndTime != nil:
		to = *q.EndTime
		from = to.AddDate(-1, 0, 0)
	default:
		from = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		to = from.AddDate(2, 0, 0)
	}
	if !notabledays.ValidWindow(from, to) {
		return from, to, notabledays.ErrInvalid
	}
	return from.UTC(), to.UTC(), nil
}
func expandEvents(events []notabledays.Event, from, to time.Time) ([]notabledays.Event, error) {
	out := []notabledays.Event{}
	for _, v := range events {
		values, e := notabledays.Expand(v, from, to)
		if e != nil {
			return nil, e
		}
		out = append(out, values...)
		if len(out) > notabledays.MaxResults {
			return nil, notabledays.ErrLimit
		}
	}
	return out, nil
}
