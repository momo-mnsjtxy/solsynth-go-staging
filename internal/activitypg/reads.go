// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
)

func validID(s string) bool { return authority.ValidID(s) }

type predicate struct {
	sql  string
	args []any
}

func (p *predicate) add(expr string, v any) {
	p.args = append(p.args, v)
	p.sql += " AND " + strings.ReplaceAll(expr, "?", fmt.Sprintf("$%d", len(p.args)))
}
func filters(actor string, f activities.Filter, catalog bool) predicate {
	p := predicate{sql: "account_id=$1::uuid AND deleted_at IS NULL", args: []any{actor}}
	if f.Provider != "" {
		p.add("document->>'provider'=?::text", f.Provider)
	}
	if f.Query != "" {
		fields := []string{"title", "subtitle", "caption", "manual_id", "provider", "reference_id"}
		if catalog {
			fields = []string{"name"}
		}
		terms := []string{}
		for _, field := range fields {
			expr := "document->>'" + field + "'"
			if field == "manual_id" || field == "name" {
				expr = field
			}
			terms = append(terms, "position(?::text in COALESCE("+expr+",''))>0")
		}
		p.add("("+strings.Join(terms, " OR ")+")", f.Query) // Exact normalized queryable terms are an additional OR arm below.
		last := fmt.Sprintf("$%d", len(p.args))
		p.sql = strings.TrimSuffix(p.sql, ")") + " OR COALESCE(document->'queryable_terms','[]'::jsonb) @> jsonb_build_array(lower(btrim(" + last + "::text))))"
	}
	for _, tag := range f.Tags {
		p.add("EXISTS(SELECT 1 FROM jsonb_array_elements(document->'tags') tag WHERE tag->>'slug'=?::text)", tag)
	}
	if catalog {
		if f.IsActive != nil {
			if *f.IsActive {
				p.sql += " AND last_active_at>clock_timestamp()-interval '24 hours'"
			} else {
				p.sql += " AND(last_active_at IS NULL OR last_active_at<=clock_timestamp()-interval '24 hours')"
			}
		}
		return p
	}
	if f.Type != "" {
		p.add("document->>'type'=?::text", f.Type)
	}
	if f.ReferenceID != "" {
		p.add("document->>'reference_id'=?::text", f.ReferenceID)
	}
	if f.Term != "" {
		p.add("COALESCE(document->'queryable_terms','[]'::jsonb) @> jsonb_build_array(?::text)", strings.ToLower(f.Term))
	}
	if f.CatalogID != "" {
		if !validID(f.CatalogID) {
			p.sql += " AND false"
		} else {
			p.add("catalog_id=?::uuid", f.CatalogID)
		}
	}
	if f.Visibility != nil {
		p.add("visibility=?::integer", *f.Visibility)
	}
	if f.From != nil {
		p.add("started_at>=?::timestamptz", *f.From)
	}
	if f.To != nil {
		p.add("started_at<?::timestamptz", *f.To)
	}
	if !f.IncludeExpired {
		p.sql += " AND lease_expires_at>clock_timestamp() AND ended_at IS NULL"
	}
	return p
}
func validateFilter(f activities.Filter) error {
	if f.Offset < 0 || f.Offset > 100000 || f.Take < 1 || f.Take > 200 || len(f.Tags) > 64 {
		return activities.ErrInvalid
	}
	return nil
}
func (r *Repository) List(ctx context.Context, actor, reference string, f activities.Filter) (out []activities.Activity, total int, e error) {
	out = []activities.Activity{}
	if e = validateFilter(f); e != nil {
		return
	}
	tx, target, _, e := r.begin(ctx, actor, reference, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	if actor != target {
		f = activities.Filter{Take: 20}
	}
	allow, friend, e := audience(ctx, tx, actor, target)
	if e != nil {
		return
	}
	if !allow {
		if _, e = r.check(ctx, tx, actor, ""); e == nil {
			e = tx.Commit()
		}
		return
	}
	p := filters(target, f, false)
	if actor != target {
		p.sql += " AND (visibility=1"
		if friend {
			p.sql += " OR visibility=2"
		}
		p.sql += ")"
	}
	// Active lists are bounded as a whole; filtered/history lists are paginated.
	paged := f.Filtered || f.IncludeExpired
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_presence_activities WHERE `+p.sql, p.args...).Scan(&total); e != nil {
		return
	}
	limit, offset := 1001, 0
	if paged {
		limit, offset = f.Take, f.Offset
	}
	p.args = append(p.args, limit, offset)
	rs, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE `+p.sql+fmt.Sprintf(` ORDER BY COALESCE(started_at,created_at) DESC,id LIMIT $%d::integer OFFSET $%d::integer`, len(p.args)-1, len(p.args)), p.args...)
	if e != nil {
		return
	}
	all := []row{}
	for rs.Next() {
		v, x := scan(rs)
		if x != nil {
			rs.Close()
			return nil, 0, x
		}
		all = append(all, v)
	}
	e = rs.Err()
	rs.Close()
	if e != nil {
		return
	}
	if !paged && len(all) > 1000 {
		return nil, 0, activities.ErrUnavailable
	}
	for i := range all {
		if e = artwork(ctx, tx, target, &all[i].Content, false); e != nil {
			return
		}
	}
	// A later artwork lock can consume an earlier reference's retention window.
	// With the full lock set held, refresh every retained reference once more.
	for i := range all {
		if e = artwork(ctx, tx, target, &all[i].Content, false); e != nil {
			return
		}
	}
	// Artwork association/file contention is the last domain lock wait. Recheck
	// the original reader proof before the current audience and source sessions.
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	allow, friend, e = audience(ctx, tx, actor, target)
	if e != nil {
		return
	}
	if allow {
		for _, v := range all {
			if actor != target && v.Visibility != activities.Public && !(v.Visibility == activities.Friends && friend) {
				continue
			}
			if !f.IncludeExpired {
				ok, x := r.eligible(ctx, tx, v)
				if x != nil {
					return nil, 0, x
				}
				if !ok {
					continue
				}
			}
			out = append(out, v.Activity)
		}
	}
	// Last waits precede the proof and clock fence. An expired lease is never
	// exposed because a previous snapshot saw it active before lock contention.
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	now, e := clock(ctx, tx)
	if e != nil {
		return nil, 0, e
	}
	if !f.IncludeExpired {
		live := out[:0]
		for _, v := range out {
			if v.EndedAt == nil && now.Before(v.LeaseExpiresAt) {
				live = append(live, v)
			}
		}
		out = live
	}
	e = tx.Commit()
	return
}

const catalogFields = `id::text,account_id::text,catalog_key,name,document,visibility,total_seconds,session_count,last_active_at,created_at,updated_at,deleted_at`

func scanCatalog(s scanner) (v activities.Catalog, e error) {
	var data []byte
	e = s.Scan(&v.ID, &v.AccountID, &v.CatalogKey, &v.Name, &data, &v.Visibility, &v.TotalSeconds, &v.SessionCount, &v.LastActiveAt, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if e == nil {
		e = json.Unmarshal(data, &v.Content)
	}
	activities.NormalizeContent(&v.Content)
	return
}
func (r *Repository) Catalog(ctx context.Context, actor string, f activities.Filter) (out []activities.Catalog, e error) {
	out = []activities.Catalog{}
	if actor == "" {
		return out, authority.ErrInvalid
	}
	if e = validateFilter(f); e != nil {
		return
	}
	tx, _, _, e := r.begin(ctx, actor, actor, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	p := filters(actor, f, true)
	p.args = append(p.args, f.Take, f.Offset)
	rows, e := tx.QueryContext(ctx, `SELECT `+catalogFields+` FROM owned_activity_catalog WHERE `+p.sql+fmt.Sprintf(` ORDER BY COALESCE(last_active_at,updated_at) DESC,id LIMIT $%d::integer OFFSET $%d::integer`, len(p.args)-1, len(p.args)), p.args...)
	if e != nil {
		return
	}
	for rows.Next() {
		v, x := scanCatalog(rows)
		if x != nil {
			rows.Close()
			return nil, x
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	for i := range out {
		if e = artwork(ctx, tx, actor, &out[i].Content, false); e != nil {
			return nil, e
		}
	}
	for i := range out {
		if e = artwork(ctx, tx, actor, &out[i].Content, false); e != nil {
			return nil, e
		}
	}
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, e
	}
	if f.IsActive != nil {
		now, x := clock(ctx, tx)
		if x != nil {
			return nil, x
		}
		filtered := out[:0]
		for _, v := range out {
			active := v.LastActiveAt != nil && v.LastActiveAt.After(now.Add(-24*time.Hour))
			if active == *f.IsActive {
				filtered = append(filtered, v)
			}
		}
		out = filtered
	}
	e = tx.Commit()
	return
}
func (r *Repository) Stats(ctx context.Context, actor string, f activities.Filter, byTag bool) (out map[string]int64, e error) {
	out = map[string]int64{}
	if actor == "" {
		return nil, authority.ErrInvalid
	}
	tx, _, _, e := r.begin(ctx, actor, actor, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	p := predicate{sql: "a.account_id=$1::uuid AND a.deleted_at IS NULL AND a.ended_at IS NOT NULL AND a.started_at IS NOT NULL", args: []any{actor}}
	if f.From != nil {
		p.add("a.ended_at>=?::timestamptz", *f.From)
	}
	if f.To != nil {
		p.add("a.ended_at<?::timestamptz", *f.To)
	}
	join := ""
	key := "a.catalog_id::text"
	if byTag {
		join = " CROSS JOIN LATERAL jsonb_array_elements(a.document->'tags') tag"
		key = "tag->>'slug'"
	} else {
		p.sql += " AND a.catalog_id IS NOT NULL"
		for _, tag := range f.Tags {
			p.add("EXISTS(SELECT 1 FROM owned_activity_catalog c CROSS JOIN LATERAL jsonb_array_elements(c.document->'tags') tag WHERE c.id=a.catalog_id AND c.account_id=a.account_id AND c.deleted_at IS NULL AND tag->>'slug'=?::text)", tag)
		}
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+key+`,sum(GREATEST(0,floor(extract(epoch FROM (a.ended_at-a.started_at)))))::bigint FROM owned_presence_activities a`+join+` WHERE `+p.sql+` GROUP BY `+key+` ORDER BY `+key, p.args...)
	if e != nil {
		return
	}
	for rows.Next() {
		var k string
		var v int64
		if e = rows.Scan(&k, &v); e != nil {
			rows.Close()
			return nil, e
		}
		out[k] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, e
	}
	e = tx.Commit()
	return
}
