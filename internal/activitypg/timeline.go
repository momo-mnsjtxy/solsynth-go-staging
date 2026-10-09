// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/passport/history"
	"solsynth.local/go-migration/ports/passport/presence"
	"sort"
	"strings"
)

// Timeline reads the very same rows written by canonical presence/activity
// mutations. Expired and ended activities remain history; deleted ones do not.
func (r *Repository) Timeline(ctx context.Context, actor, reference string, f history.Filter) (out []history.TimelineItem, total int, e error) {
	out = []history.TimelineItem{}
	if e = history.Validate(f); e != nil {
		return
	}
	tx, target, _, e := r.begin(ctx, actor, reference, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	allow, friend, e := audience(ctx, tx, actor, target)
	if e != nil {
		return
	}
	if !allow {
		if _, e = r.check(ctx, tx, actor, ""); e != nil {
			return nil, 0, e
		}
		return out, 0, tx.Commit()
	}
	const candidates = `SELECT id,created_at,0 AS kind FROM owned_presence_statuses WHERE account_id=$1::uuid AND($2::boolean OR type<>3)
 UNION ALL SELECT id,created_at,1 AS kind FROM owned_presence_activities WHERE account_id=$1::uuid AND deleted_at IS NULL AND($2::boolean OR visibility=1 OR $3::boolean AND visibility=2)`
	rows, e := tx.QueryContext(ctx, `WITH candidates AS(`+candidates+`),filtered AS MATERIALIZED(SELECT * FROM candidates WHERE($4::timestamptz IS NULL OR created_at>=$4::timestamptz)AND($5::timestamptz IS NULL OR created_at<$5::timestamptz)),page AS(SELECT * FROM filtered ORDER BY created_at DESC,kind,id LIMIT $6::integer OFFSET $7::integer) SELECT totals.n,page.id::text,page.created_at,page.kind FROM(SELECT count(*) AS n FROM filtered)totals LEFT JOIN page ON true ORDER BY page.created_at DESC,page.kind,page.id`, target, actor == target, friend, f.From, f.To, f.Take, f.Offset)
	if e != nil {
		return
	}
	for rows.Next() {
		var id sql.NullString
		var created sql.NullTime
		var kind sql.NullInt64
		if e = rows.Scan(&total, &id, &created, &kind); e != nil {
			break
		}
		if id.Valid {
			out = append(out, history.TimelineItem{ID: id.String, CreatedAt: created.Time, EventType: int(kind.Int64)})
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		v := &out[i]
		if v.EventType == 0 {
			s := presence.Status{IsCustomized: true}
			var meta []byte
			e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,attitude,type,label,symbol,app_identifier,is_automated,meta,cleared_at,created_at,updated_at FROM owned_presence_statuses WHERE id=$1::uuid AND account_id=$2::uuid`, v.ID, target).Scan(&s.ID, &s.AccountID, &s.Attitude, &s.Type, &s.Label, &s.Symbol, &s.AppIdentifier, &s.IsAutomated, &meta, &s.ClearedAt, &s.CreatedAt, &s.UpdatedAt)
			if e != nil {
				return nil, 0, e
			}
			if actor == target && len(meta) > 0 {
				s.Meta = json.RawMessage(meta)
			} else {
				s.Meta = json.RawMessage(`null`)
			}
			v.Status = &s
		} else {
			var a row
			a, e = scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid AND deleted_at IS NULL`, v.ID, target))
			if e != nil {
				return nil, 0, e
			}
			if actor != target {
				a.Meta = json.RawMessage(`null`)
				a.ManualID = nil
			}
			v.Activity = &a.Activity
		}
	}
	// A historical event is not a live source-session admission. Source logout,
	// authentication expiry and activity-lease expiry do not remove history.
	// Current account/audience, per-record visibility and artwork rights still do.
	// Acquire all association hashes globally before any media-file lock: events
	// from different pages may mention the same hashes in opposite orders.
	hashes := []string{}
	for _, event := range out {
		if a := event.Activity; a != nil {
			for _, hash := range []*string{a.LargeImage, a.SmallImage} {
				if hash != nil && strings.HasPrefix(*hash, "sha256:") {
					hashes = append(hashes, *hash)
				}
			}
		}
	}
	sort.Strings(hashes)
	for i, hash := range hashes {
		if i > 0 && hashes[i-1] == hash {
			continue
		}
		if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('presence-artwork:' || $1::text,0))`, hash); e != nil {
			return nil, 0, e
		}
	}
	for i := range out {
		if a := out[i].Activity; a != nil {
			if e = artwork(ctx, tx, target, &a.Content, false); e != nil {
				return nil, 0, e
			}
		}
	}
	// Later artwork waits may expire earlier associations. Recheck all references
	// with the complete lock set, then the original reader proof and audience.
	for i := range out {
		if a := out[i].Activity; a != nil {
			if e = artwork(ctx, tx, target, &a.Content, false); e != nil {
				return nil, 0, e
			}
		}
	}
	if _, e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	currentAllow, currentFriend, e := audience(ctx, tx, actor, target)
	if e != nil {
		return nil, 0, e
	}
	if !currentAllow || friend != currentFriend {
		return nil, 0, history.ErrNotFound
	}
	// No devices, auth session identifiers, actor objects or message/media nested
	// payloads are synthesized for historical events. Meta is owner-only.
	e = tx.Commit()
	return
}

var _ history.TimelineRepository = (*Repository)(nil)
