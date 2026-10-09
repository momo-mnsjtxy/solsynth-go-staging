// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
)

func (r *Repository) LocationEvents(ctx context.Context, actor, id string, pin bool, origin *locations.Point, since uint64, limit int) ([]locations.LocationEvent, uint64, error) {
	if !authority.ValidID(id) || limit < 0 || limit > 100 || since > 1<<63-1 {
		return nil, 0, locations.ErrInvalid
	}
	tx, _, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	check := func() error {
		if pin {
			p, e := r.loadPin(ctx, tx, id, false)
			if e != nil {
				return e
			}
			ok, e := r.accessPin(ctx, tx, p, actor)
			if e != nil {
				return e
			}
			if !ok {
				return locations.ErrNotFound
			}
		} else {
			m, e := r.loadMeet(ctx, tx, id, false)
			if e != nil {
				return e
			}
			ok, e := r.accessMeet(ctx, tx, m, actor, origin)
			if e != nil {
				return e
			}
			if !ok {
				return locations.ErrNotFound
			}
		}
		return nil
	}
	if e = check(); e != nil {
		return nil, 0, e
	}
	// Parent locks are held while the current social predicates are refreshed.
	// Read child-pin event visibility only after these potentially waiting checks.
	if e = check(); e != nil {
		return nil, 0, e
	}
	if e = r.recheck(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	var latest uint64
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(max(a.id),0) FROM owned_location_audit a WHERE `+locationAuditAudience, id, pin, actor).Scan(&latest)
	if e != nil {
		return nil, 0, e
	}
	out := []locations.LocationEvent{}
	cursor := latest
	if limit > 0 {
		cursor = since
		rows, e := tx.QueryContext(ctx, `SELECT a.id,a.action,a.occurred_at FROM owned_location_audit a WHERE a.id>$4 AND (`+locationAuditAudience+`) ORDER BY a.id LIMIT $5`, id, pin, actor, since, limit)
		if e != nil {
			return nil, 0, e
		}
		for rows.Next() {
			var v locations.LocationEvent
			var action string
			if e = rows.Scan(&v.Sequence, &action, &v.OccurredAt); e != nil {
				rows.Close()
				return nil, 0, e
			}
			v.Type = strings.ReplaceAll(action, ".", "_")
			switch action {
			case "meet.join":
				v.Type = "participant_joined"
			case "meet.complete":
				v.Type = "completed"
			case "meet.visibility":
				v.Type = "visibility_updated"
			case "pin.create", "pin.update":
				v.Type = "pin_updated"
			case "pin.disconnect":
				v.Type = "pin_offline"
			}
			out = append(out, v)
			cursor = v.Sequence
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, 0, e
		}
	}
	if e = check(); e != nil {
		return nil, 0, e
	}
	if e = r.recheck(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	filtered := out[:0]
	for _, event := range out {
		var visible bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_location_audit a WHERE a.id=$4 AND (`+locationAuditAudience+`))`, id, pin, actor, event.Sequence).Scan(&visible); e != nil {
			return nil, 0, e
		}
		if visible {
			filtered = append(filtered, event)
		}
	}
	return filtered, cursor, tx.Commit()
}

const locationAuditAudience = `a.resource_id=$1 OR (NOT $2 AND EXISTS(SELECT 1 FROM owned_location_pins p JOIN owned_authority_accounts owner ON owner.id=p.account_id WHERE p.id=a.resource_id AND p.meet_id=$1 AND p.deleted_at IS NULL AND NOT owner.disabled AND ((p.status=0 AND (p.last_heartbeat_at>clock_timestamp()-interval '5 minutes' OR p.keep_on_disconnect AND p.last_heartbeat_at>clock_timestamp()-interval '24 hours 5 minutes')) OR(p.status=1 AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))) AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships b WHERE b.status=-100 AND ((b.account_id=$3 AND b.related_id=p.account_id) OR (b.account_id=p.account_id AND b.related_id=$3))) AND(p.account_id=$3 OR p.visibility=0 OR(p.visibility=2 AND EXISTS(SELECT 1 FROM owned_social_effective_relationships x WHERE x.account_id=$3 AND x.related_id=p.account_id AND x.status>=100) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships y WHERE y.account_id=p.account_id AND y.related_id=$3 AND y.status>=100)))))`
