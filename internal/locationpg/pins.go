// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
	"sort"
	"time"
)

func (r *Repository) loadPin(ctx context.Context, tx *sql.Tx, id string, write bool) (locations.Pin, error) {
	var p locations.Pin
	if !authority.ValidID(id) {
		return p, locations.ErrNotFound
	}
	// A meet is always locked before any of its pins, matching meet mutation
	// and projection order. This avoids read/update lock inversions.
	var meetID *string
	if e := tx.QueryRowContext(ctx, `SELECT meet_id::text FROM owned_location_pins WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&meetID); e != nil {
		return p, mapped(e)
	}
	if meetID != nil {
		if _, e := r.loadMeet(ctx, tx, *meetID, false); e != nil {
			return p, e
		}
	}
	var lon, lat *float64
	var metadata []byte
	lock := " FOR SHARE OF p"
	if write {
		lock = " FOR UPDATE OF p"
	}
	e := tx.QueryRowContext(ctx, `SELECT p.id::text,p.account_id::text,p.session_id::text,p.meet_id::text,p.visibility,p.status,p.location_name,p.location_address,p.longitude,p.latitude,p.metadata,p.keep_on_disconnect,p.last_heartbeat_at,p.expires_at,p.created_at,p.updated_at FROM owned_location_pins p JOIN owned_authority_accounts a ON a.id=p.account_id WHERE p.id=$1 AND p.deleted_at IS NULL AND p.status<>2 AND NOT a.disabled`+lock, id).Scan(&p.ID, &p.AccountID, &p.DeviceID, &p.MeetID, &p.Visibility, &p.Status, &p.LocationName, &p.LocationAddress, &lon, &lat, &metadata, &p.KeepOnDisconnect, &p.LastHeartbeatAt, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt)
	if e != nil {
		return p, mapped(e)
	}
	if e = json.Unmarshal(metadata, &p.Metadata); e != nil {
		return p, e
	}
	p.LocationWKT = wkt(lon, lat)
	now := r.now().UTC()
	if p.Status == locations.PinActive && !now.Before(p.LastHeartbeatAt.Add(5*time.Minute)) {
		if p.KeepOnDisconnect {
			p.Status = locations.PinOffline
			expiry := p.LastHeartbeatAt.Add(5*time.Minute + 24*time.Hour)
			p.ExpiresAt = &expiry
		} else {
			p.Status = locations.PinRemoved
		}
	}
	if p.Status == locations.PinOffline && p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
		p.Status = locations.PinRemoved
	}
	p.Account, e = account(ctx, tx, p.AccountID)
	return p, e
}
func (r *Repository) accessPin(ctx context.Context, tx *sql.Tx, p locations.Pin, actor string) (bool, error) {
	if p.Status == locations.PinRemoved {
		return false, nil
	}
	blocked, friend, e := socialState(ctx, tx, actor, p.AccountID)
	if e != nil || blocked {
		return false, e
	}
	if p.MeetID != nil {
		m, e := r.loadMeet(ctx, tx, *p.MeetID, false)
		if errors.Is(e, locations.ErrNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		ok, e := r.accessMeet(ctx, tx, m, actor, nil)
		if e != nil || !ok {
			return false, e
		}
	}
	return p.AccountID == actor || p.Visibility == locations.Public || p.Visibility == locations.Unlisted && friend, nil
}
func (r *Repository) Pin(ctx context.Context, actor, id string) (locations.Pin, error) {
	tx, _, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return locations.Pin{}, e
	}
	defer tx.Rollback()
	p, e := r.loadPin(ctx, tx, id, false)
	if e != nil {
		return p, e
	}
	ok, e := r.accessPin(ctx, tx, p, actor)
	if e != nil {
		return locations.Pin{}, e
	}
	if !ok {
		return locations.Pin{}, locations.ErrNotFound
	}
	if p.AccountID != actor {
		p.DeviceID = ""
	}
	return p, tx.Commit()
}
func (r *Repository) PutPin(ctx context.Context, actor, meet, id string, v locations.PinRequest) (locations.Pin, error) {
	if e := locations.ValidatePin(v); e != nil {
		return locations.Pin{}, e
	}
	permission := "location.pins.create"
	if id != "" {
		permission = "location.pins.update"
	}
	if meet != "" {
		permission = "meet.pin.manage"
	}
	tx, proof, e := r.begin(ctx, actor, permission, true)
	if e != nil {
		return locations.Pin{}, e
	}
	defer tx.Rollback()
	if !authority.ValidID(proof.Session.ID) {
		return locations.Pin{}, locations.ErrUnavailable
	}
	if meet != "" {
		m, e := r.loadMeet(ctx, tx, meet, true)
		if e != nil {
			return locations.Pin{}, e
		}
		if !participant(m, actor) {
			return locations.Pin{}, locations.ErrForbidden
		}
		if m.Status != locations.Active {
			return locations.Pin{}, locations.ErrConflict
		}
		blocked, _, e := socialState(ctx, tx, actor, m.HostID)
		if e != nil {
			return locations.Pin{}, e
		}
		if blocked {
			return locations.Pin{}, locations.ErrNotFound
		}
	}
	explicit := id != ""
	if !explicit {
		e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_location_pins WHERE account_id=$1 AND meet_id IS NOT DISTINCT FROM NULLIF($2,'')::uuid AND status=0 AND deleted_at IS NULL`, actor, meet).Scan(&id)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return locations.Pin{}, e
		}
	}
	now := r.now().UTC()
	p := locations.Pin{AccountID: actor, DeviceID: proof.Session.ID, Visibility: locations.Private, Status: locations.PinActive, KeepOnDisconnect: true, LastHeartbeatAt: now, CreatedAt: now, UpdatedAt: now, Metadata: map[string]json.RawMessage{}}
	if meet != "" {
		p.MeetID = &meet
	}
	existing := false
	if id != "" {
		old, e := r.loadPin(ctx, tx, id, true)
		if e != nil {
			return locations.Pin{}, e
		}
		if old.AccountID != actor || explicit && old.DeviceID != proof.Session.ID {
			return locations.Pin{}, locations.ErrNotFound
		}
		if explicit && (old.MeetID == nil) != (meet == "") {
			return locations.Pin{}, locations.ErrInvalid
		}
		if now.Before(old.LastHeartbeatAt.Add(5 * time.Second)) {
			return locations.Pin{}, locations.ErrRateLimit
		}
		if explicit && old.Status != locations.PinActive {
			return locations.Pin{}, locations.ErrConflict
		}
		reuse := explicit
		if !reuse && old.LocationWKT != nil && v.LocationWKT != nil && *v.LocationWKT != "" {
			a, _ := locations.ParsePoint(*old.LocationWKT)
			b, _ := locations.ParsePoint(*v.LocationWKT)
			reuse = locations.Distance(a, b) < 10
		}
		if reuse {
			p = old
			p.DeviceID = proof.Session.ID
			p.Status = locations.PinActive
			p.ExpiresAt = nil
			existing = true
		} else {
			expiry := now.Add(24 * time.Hour)
			status := locations.PinOffline
			if !old.KeepOnDisconnect {
				status = locations.PinRemoved
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_location_pins SET status=$2,expires_at=$3,updated_at=$4 WHERE id=$1`, old.ID, status, expiry, now); e != nil {
				return locations.Pin{}, e
			}
		}
	}
	if v.Visibility != nil {
		p.Visibility = *v.Visibility
	}
	if v.LocationName != nil {
		p.LocationName = v.LocationName
	}
	if v.LocationAddress != nil {
		p.LocationAddress = v.LocationAddress
	}
	if v.LocationWKT != nil {
		p.LocationWKT = v.LocationWKT
	}
	if v.Metadata != nil {
		p.Metadata = v.Metadata
	}
	if v.KeepOnDisconnect != nil {
		p.KeepOnDisconnect = *v.KeepOnDisconnect
	}
	lon, lat, normalized, e := coordinates(p.LocationWKT)
	if e != nil {
		return locations.Pin{}, e
	}
	p.LocationWKT = normalized
	p.LastHeartbeatAt = now
	p.UpdatedAt = now
	raw, e := meta(p.Metadata)
	if e != nil {
		return locations.Pin{}, e
	}
	if existing {
		_, e = tx.ExecContext(ctx, `UPDATE owned_location_pins SET session_id=$2,visibility=$3,location_name=$4,location_address=$5,longitude=$6,latitude=$7,metadata=$8,keep_on_disconnect=$9,last_heartbeat_at=$10,updated_at=$10,status=0,expires_at=NULL WHERE id=$1`, p.ID, p.DeviceID, p.Visibility, p.LocationName, p.LocationAddress, lon, lat, raw, p.KeepOnDisconnect, now)
	} else {
		p.ID, e = uid()
		if e != nil {
			return locations.Pin{}, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_location_pins(id,account_id,session_id,meet_id,visibility,status,location_name,location_address,longitude,latitude,metadata,keep_on_disconnect,last_heartbeat_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,0,$6,$7,$8,$9,$10,$11,$12,$12,$12)`, p.ID, actor, proof.Session.ID, p.MeetID, p.Visibility, p.LocationName, p.LocationAddress, lon, lat, raw, p.KeepOnDisconnect, now)
	}
	if e != nil {
		return locations.Pin{}, mapped(e)
	}
	if meet != "" {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_meets SET expires_at=GREATEST(expires_at,$2),updated_at=$3 WHERE id=$1 AND status=0`, meet, now.Add(30*time.Minute), now); e != nil {
			return locations.Pin{}, e
		}
	}
	if e = audit(ctx, tx, actor, p.ID, "pin.put", now); e != nil {
		return locations.Pin{}, e
	}
	p.Account, e = account(ctx, tx, actor)
	if e != nil {
		return locations.Pin{}, e
	}
	return p, tx.Commit()
}
func (r *Repository) pins(ctx context.Context, tx *sql.Tx, actor, meet string, p locations.Page, own bool) ([]locations.Pin, int64, error) {
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_location_pins WHERE deleted_at IS NULL AND status<>2 AND ($1='' OR meet_id::text=$1) AND (NOT $2 OR account_id=$3) ORDER BY created_at DESC,id`, meet, own, actor)
	if e != nil {
		return nil, 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	out := []locations.Pin{}
	distance := map[string]float64{}
	for _, id := range ids {
		v, e := r.loadPin(ctx, tx, id, false)
		if errors.Is(e, locations.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		ok, e := r.accessPin(ctx, tx, v, actor)
		if e != nil {
			return nil, 0, e
		}
		if !ok || p.Visibility != nil && v.Visibility != *p.Visibility {
			continue
		}
		if p.Origin != nil {
			if v.LocationWKT == nil {
				continue
			}
			point, e := locations.ParsePoint(*v.LocationWKT)
			if e != nil {
				return nil, 0, e
			}
			d := locations.Distance(*p.Origin, point)
			if p.Radius > 0 && d > p.Radius {
				continue
			}
			distance[v.ID] = d
		}
		if v.AccountID != actor {
			v.DeviceID = ""
		}
		out = append(out, v)
	}
	if p.Origin != nil {
		sort.SliceStable(out, func(i, j int) bool { return distance[out[i].ID] < distance[out[j].ID] })
	}
	total := int64(len(out))
	if p.Offset >= len(out) {
		out = []locations.Pin{}
	} else {
		out = out[p.Offset:]
		if len(out) > p.Take {
			out = out[:p.Take]
		}
	}
	return out, total, nil
}
func (r *Repository) Pins(ctx context.Context, actor, meet string, p locations.Page) ([]locations.Pin, int64, error) {
	if !pageOK(p) {
		return nil, 0, locations.ErrInvalid
	}
	tx, _, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if meet != "" {
		m, e := r.loadMeet(ctx, tx, meet, false)
		if e != nil {
			return nil, 0, e
		}
		ok, e := r.accessMeet(ctx, tx, m, actor, p.Origin)
		if e != nil {
			return nil, 0, e
		}
		if !ok {
			return nil, 0, locations.ErrNotFound
		}
	}
	out, total, e := r.pins(ctx, tx, actor, meet, p, p.HostOnly)
	if e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}
func (r *Repository) Disconnect(ctx context.Context, actor, id string, keep bool) error {
	return r.endPin(ctx, actor, id, keep, false)
}
func (r *Repository) DeletePin(ctx context.Context, actor, id string) error {
	return r.endPin(ctx, actor, id, false, true)
}

func (r *Repository) DeleteMeetPin(ctx context.Context, actor, meet string) error {
	tx, proof, e := r.begin(ctx, actor, "meet.pin.manage", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	m, e := r.loadMeet(ctx, tx, meet, false)
	if e != nil {
		return e
	}
	if !participant(m, actor) {
		return locations.ErrForbidden
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_location_pins WHERE meet_id=$1 AND account_id=$2 AND session_id=$3 AND status=0 AND deleted_at IS NULL FOR UPDATE`, meet, actor, proof.Session.ID).Scan(&id)
	if e != nil {
		return mapped(e)
	}
	now := r.now().UTC()
	if _, e = tx.ExecContext(ctx, `UPDATE owned_location_pins SET status=2,updated_at=$2 WHERE id=$1`, id, now); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, id, "pin.delete", now); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) endPin(ctx context.Context, actor, id string, keep, remove bool) error {
	tx, p, e := r.begin(ctx, actor, "location.pins.delete", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	pin, e := r.loadPin(ctx, tx, id, true)
	if e != nil {
		return e
	}
	if pin.AccountID != actor || pin.DeviceID != p.Session.ID {
		return locations.ErrNotFound
	}
	now := r.now().UTC()
	status := locations.PinRemoved
	var expiry *time.Time
	if keep && !remove {
		status = locations.PinOffline
		v := now.Add(24 * time.Hour)
		expiry = &v
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_location_pins SET status=$2,expires_at=$3,keep_on_disconnect=$4,updated_at=$5 WHERE id=$1`, id, status, expiry, keep, now); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, id, "pin.disconnect", now); e != nil {
		return e
	}
	return tx.Commit()
}

// Expire materializes only deadline-derived terminal states. Reads already
// enforce the same deadlines after a restart; no in-memory scheduler is needed.
func (r *Repository) Expire(ctx context.Context, limit int) (int64, error) {
	if r == nil || r.db == nil || limit < 1 || limit > 1000 {
		return 0, locations.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	now := r.now().UTC()
	res, e := tx.ExecContext(ctx, `WITH due AS (SELECT id FROM owned_meets WHERE status=0 AND expires_at<=$1 AND deleted_at IS NULL ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) UPDATE owned_meets SET status=2,updated_at=$1 WHERE id IN(SELECT id FROM due)`, now, limit)
	if e != nil {
		return 0, e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return 0, e
	}
	res, e = tx.ExecContext(ctx, `WITH due AS (SELECT id FROM owned_location_pins WHERE deleted_at IS NULL AND ((status=0 AND last_heartbeat_at<=$1::timestamptz-interval '5 minutes') OR(status=1 AND expires_at<=$1)) ORDER BY last_heartbeat_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) UPDATE owned_location_pins SET status=CASE WHEN status=1 OR NOT keep_on_disconnect OR last_heartbeat_at<=$1::timestamptz-interval '24 hours 5 minutes' THEN 2 ELSE 1 END,expires_at=CASE WHEN status=0 AND keep_on_disconnect THEN last_heartbeat_at+interval '24 hours 5 minutes' ELSE expires_at END,updated_at=$1 WHERE id IN(SELECT id FROM due)`, now, limit)
	if e != nil {
		return 0, e
	}
	m, e := res.RowsAffected()
	if e != nil {
		return 0, e
	}
	return n + m, tx.Commit()
}
