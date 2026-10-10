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

func (r *Repository) loadMeet(ctx context.Context, tx *sql.Tx, id string, write bool) (locations.Meet, error) {
	var m locations.Meet
	if !authority.ValidID(id) {
		return m, locations.ErrNotFound
	}
	var lon, lat *float64
	var metadata []byte
	lock := " FOR SHARE OF m"
	if write {
		lock = " FOR UPDATE OF m"
	}
	e := tx.QueryRowContext(ctx, `SELECT m.id::text,m.host_id::text,m.status,m.visibility,m.notes,m.location_name,m.location_address,m.longitude,m.latitude,m.metadata,m.expires_at,m.completed_at,m.created_at,m.updated_at,m.image_id::text FROM owned_meets m JOIN owned_authority_accounts a ON a.id=m.host_id WHERE m.id=$1 AND m.deleted_at IS NULL AND NOT a.disabled`+lock, id).Scan(&m.ID, &m.HostID, &m.Status, &m.Visibility, &m.Notes, &m.LocationName, &m.LocationAddress, &lon, &lat, &metadata, &m.ExpiresAt, &m.CompletedAt, &m.CreatedAt, &m.UpdatedAt, &m.ImageID)
	if e != nil {
		return m, mapped(e)
	}
	m.LocationWKT = wkt(lon, lat)
	if e = json.Unmarshal(metadata, &m.Metadata); e != nil {
		return m, e
	}
	if m.Status == locations.Active && !r.now().UTC().Before(m.ExpiresAt) {
		m.Status = locations.Expired
	}
	m.Host, e = account(ctx, tx, m.HostID)
	if e != nil {
		return m, e
	}
	m.Participants = []locations.Participant{}
	m.Pins = []locations.Pin{}
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text,p.account_id::text,p.joined_at,a.id::text,a.name,a.nick,a.language,a.region,a.created_at FROM owned_meet_participants p JOIN owned_authority_accounts a ON a.id=p.account_id WHERE p.meet_id=$1 AND NOT a.disabled ORDER BY p.joined_at,p.id`, id)
	if e != nil {
		return m, e
	}
	defer rows.Close()
	for rows.Next() {
		var p locations.Participant
		p.MeetID = id
		if e = rows.Scan(&p.ID, &p.AccountID, &p.JoinedAt, &p.Account.ID, &p.Account.Name, &p.Account.Nick, &p.Account.Language, &p.Account.Region, &p.Account.CreatedAt); e != nil {
			return m, e
		}
		m.Participants = append(m.Participants, p)
	}
	return m, rows.Err()
}
func participant(m locations.Meet, actor string) bool {
	if m.HostID == actor {
		return true
	}
	for _, p := range m.Participants {
		if p.AccountID == actor {
			return true
		}
	}
	return false
}
func (r *Repository) accessMeet(ctx context.Context, tx *sql.Tx, m locations.Meet, actor string, origin *locations.Point) (bool, error) {
	blocked, friend, e := socialState(ctx, tx, actor, m.HostID)
	if e != nil || blocked {
		return false, e
	}
	if participant(m, actor) {
		return true, nil
	}
	if m.Visibility == locations.Private {
		return false, nil
	}
	for _, p := range m.Participants {
		b, f, e := socialState(ctx, tx, actor, p.AccountID)
		if e != nil {
			return false, e
		}
		friend = friend || f && !b
	}
	if friend {
		return true, nil
	}
	if m.Visibility == locations.Public && origin != nil && m.LocationWKT != nil {
		point, e := locations.ParsePoint(*m.LocationWKT)
		if e != nil {
			return false, e
		}
		return locations.Distance(point, *origin) <= 5000, nil
	}
	return false, nil
}
func (r *Repository) projectMeet(ctx context.Context, tx *sql.Tx, m locations.Meet, actor string) (locations.Meet, error) {
	out := []locations.Participant{}
	for _, p := range m.Participants {
		blocked, _, e := socialState(ctx, tx, actor, p.AccountID)
		if e != nil {
			return m, e
		}
		if !blocked {
			out = append(out, p)
		}
	}
	m.Participants = out
	pins, _, e := r.pins(ctx, tx, actor, m.ID, locations.Page{Take: 100}, false)
	if e != nil {
		return m, e
	}
	m.Pins = pins
	if e = r.loadMeetImage(ctx, tx, actor, &m); e != nil {
		return m, e
	}
	if e = r.recheck(ctx, tx, actor, ""); e != nil {
		return m, e
	}
	return m, nil
}
func (r *Repository) CreateMeet(ctx context.Context, actor string, v locations.CreateMeet) (locations.Meet, error) {
	if e := locations.ValidateMeet(v); e != nil {
		return locations.Meet{}, e
	}
	tx, _, e := r.begin(ctx, actor, "meet.create", true)
	if e != nil {
		return locations.Meet{}, e
	}
	defer tx.Rollback()
	if e = r.validateMeetImage(ctx, tx, actor, v.ImageID); e != nil {
		return locations.Meet{}, e
	}
	if e = r.recheck(ctx, tx, actor, "meet.create"); e != nil {
		return locations.Meet{}, e
	}
	now := r.now().UTC()
	vis := locations.Private
	if v.Visibility != nil {
		vis = *v.Visibility
	}
	ttl := 1800
	if v.ExpiresInSeconds != nil {
		ttl = *v.ExpiresInSeconds
	}
	lon, lat, _, e := coordinates(v.LocationWKT)
	if e != nil {
		return locations.Meet{}, e
	}
	raw, e := meta(v.Metadata)
	if e != nil {
		return locations.Meet{}, e
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_meets WHERE host_id=$1 AND status=0 AND deleted_at IS NULL AND expires_at>$2 AND created_at>=$2::timestamptz-interval '5 minutes' AND visibility=$3 AND notes IS NOT DISTINCT FROM $4 AND location_name IS NOT DISTINCT FROM $5 AND location_address IS NOT DISTINCT FROM $6 AND longitude IS NOT DISTINCT FROM $7 AND latitude IS NOT DISTINCT FROM $8 AND metadata=$9::jsonb AND image_id IS NOT DISTINCT FROM $10::uuid ORDER BY created_at DESC,id LIMIT 1 FOR UPDATE`, actor, now, vis, v.Notes, v.LocationName, v.LocationAddress, lon, lat, raw, v.ImageID).Scan(&id)
	if e == nil {
		m, e := r.loadMeet(ctx, tx, id, false)
		if e != nil {
			return m, e
		}
		m, e = r.projectMeet(ctx, tx, m, actor)
		if e != nil {
			return m, e
		}
		if e = r.recheck(ctx, tx, actor, "meet.create"); e != nil {
			return m, e
		}
		return m, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return locations.Meet{}, e
	}
	id, e = uid()
	if e != nil {
		return locations.Meet{}, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_meets(id,host_id,status,visibility,notes,location_name,location_address,longitude,latitude,metadata,expires_at,created_at,updated_at,image_id) VALUES($1,$2,0,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$12)`, id, actor, vis, v.Notes, v.LocationName, v.LocationAddress, lon, lat, raw, now.Add(time.Duration(ttl)*time.Second), now, v.ImageID)
	if e != nil {
		return locations.Meet{}, e
	}
	if e = audit(ctx, tx, actor, id, "meet.create", now); e != nil {
		return locations.Meet{}, e
	}
	m, e := r.loadMeet(ctx, tx, id, false)
	if e != nil {
		return m, e
	}
	if e = r.loadMeetImage(ctx, tx, actor, &m); e != nil {
		return m, e
	}
	if e = r.recheck(ctx, tx, actor, "meet.create"); e != nil {
		return m, e
	}
	return m, tx.Commit()
}
func (r *Repository) Meet(ctx context.Context, actor, id string, origin *locations.Point) (locations.Meet, error) {
	if origin != nil {
		if _, e := locations.ParsePoint(origin.WKT()); e != nil {
			return locations.Meet{}, e
		}
	}
	tx, _, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return locations.Meet{}, e
	}
	defer tx.Rollback()
	m, e := r.loadMeet(ctx, tx, id, false)
	if e != nil {
		return m, e
	}
	ok, e := r.accessMeet(ctx, tx, m, actor, origin)
	if e != nil {
		return locations.Meet{}, e
	}
	if !ok {
		return locations.Meet{}, locations.ErrNotFound
	}
	m, e = r.projectMeet(ctx, tx, m, actor)
	if e != nil {
		return m, e
	}
	return m, tx.Commit()
}
func (r *Repository) Meets(ctx context.Context, actor string, p locations.Page) ([]locations.Meet, int64, error) {
	if !pageOK(p) {
		return nil, 0, locations.ErrInvalid
	}
	tx, _, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_meets WHERE deleted_at IS NULL AND (NOT $1 OR host_id=$2) ORDER BY created_at DESC,id`, p.HostOnly, actor)
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
	out := []locations.Meet{}
	distances := map[string]float64{}
	for _, id := range ids {
		m, e := r.loadMeet(ctx, tx, id, false)
		if errors.Is(e, locations.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		ok, e := r.accessMeet(ctx, tx, m, actor, p.Origin)
		if e != nil {
			return nil, 0, e
		}
		if !ok || p.Status != nil && m.Status != *p.Status || p.Nearby && p.Status == nil && m.Status == locations.Expired {
			continue
		}
		if p.Nearby {
			if m.LocationWKT == nil || m.Visibility == locations.Unlisted && !participant(m, actor) {
				continue
			}
			point, e := locations.ParsePoint(*m.LocationWKT)
			if e != nil {
				return nil, 0, e
			}
			d := locations.Distance(*p.Origin, point)
			if d > p.Radius {
				continue
			}
			distances[m.ID] = d
		}
		out = append(out, m)
	}
	if p.Nearby {
		sort.SliceStable(out, func(i, j int) bool { return distances[out[i].ID] < distances[out[j].ID] })
	}
	total := int64(len(out))
	if p.Offset >= len(out) {
		out = []locations.Meet{}
	} else {
		out = out[p.Offset:]
		if len(out) > p.Take {
			out = out[:p.Take]
		}
	}
	for i := range out {
		out[i], e = r.projectMeet(ctx, tx, out[i], actor)
		if e != nil {
			return nil, 0, e
		}
	}
	return out, total, tx.Commit()
}
func (r *Repository) Join(ctx context.Context, actor, id string, origin *locations.Point) (locations.Meet, error) {
	if origin != nil {
		if _, e := locations.ParsePoint(origin.WKT()); e != nil {
			return locations.Meet{}, e
		}
	}
	tx, _, e := r.begin(ctx, actor, "meet.join", true)
	if e != nil {
		return locations.Meet{}, e
	}
	defer tx.Rollback()
	m, e := r.loadMeet(ctx, tx, id, true)
	if e != nil {
		return m, e
	}
	ok, e := r.accessMeet(ctx, tx, m, actor, origin)
	if e != nil {
		return locations.Meet{}, e
	}
	if !ok {
		return locations.Meet{}, locations.ErrNotFound
	}
	if m.Status != locations.Active {
		return locations.Meet{}, locations.ErrConflict
	}
	if !participant(m, actor) {
		key, e := uid()
		if e != nil {
			return locations.Meet{}, e
		}
		now := r.now().UTC()
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_meet_participants(id,meet_id,account_id,joined_at) VALUES($1,$2,$3,$4)`, key, id, actor, now); e != nil {
			return locations.Meet{}, mapped(e)
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_meets SET updated_at=$2 WHERE id=$1`, id, now); e != nil {
			return locations.Meet{}, e
		}
		if e = audit(ctx, tx, actor, id, "meet.join", now); e != nil {
			return locations.Meet{}, e
		}
		m, e = r.loadMeet(ctx, tx, id, false)
		if e != nil {
			return m, e
		}
	}
	m, e = r.projectMeet(ctx, tx, m, actor)
	if e != nil {
		return m, e
	}
	return m, tx.Commit()
}
func (r *Repository) changeMeet(ctx context.Context, actor, id, action string, visibility int) (locations.Meet, error) {
	permission := "meet." + action
	if action == "visibility" {
		permission = "meet.visibility.update"
	}
	tx, _, e := r.begin(ctx, actor, permission, true)
	if e != nil {
		return locations.Meet{}, e
	}
	defer tx.Rollback()
	m, e := r.loadMeet(ctx, tx, id, true)
	if e != nil {
		return m, e
	}
	if m.HostID != actor {
		return locations.Meet{}, locations.ErrForbidden
	}
	if action != "delete" && m.Status != locations.Active {
		return locations.Meet{}, locations.ErrConflict
	}
	now := r.now().UTC()
	switch action {
	case "complete":
		m.Status = locations.Completed
		m.CompletedAt = &now
		_, e = tx.ExecContext(ctx, `UPDATE owned_meets SET status=1,completed_at=$2,updated_at=$2 WHERE id=$1`, id, now)
	case "visibility":
		if visibility < 0 || visibility > 2 {
			return locations.Meet{}, locations.ErrInvalid
		}
		m.Visibility = visibility
		_, e = tx.ExecContext(ctx, `UPDATE owned_meets SET visibility=$2,updated_at=$3 WHERE id=$1`, id, visibility, now)
	case "delete":
		_, e = tx.ExecContext(ctx, `UPDATE owned_meets SET deleted_at=$2,updated_at=$2 WHERE id=$1`, id, now)
	default:
		return locations.Meet{}, locations.ErrInvalid
	}
	if e != nil {
		return locations.Meet{}, e
	}
	m.UpdatedAt = now
	if e = audit(ctx, tx, actor, id, "meet."+action, now); e != nil {
		return locations.Meet{}, e
	}
	m, e = r.projectMeet(ctx, tx, m, actor)
	if e != nil {
		return m, e
	}
	return m, tx.Commit()
}
func (r *Repository) Complete(ctx context.Context, actor, id string) (locations.Meet, error) {
	return r.changeMeet(ctx, actor, id, "complete", 0)
}
func (r *Repository) Visibility(ctx context.Context, actor, id string, v int) (locations.Meet, error) {
	return r.changeMeet(ctx, actor, id, "visibility", v)
}
func (r *Repository) DeleteMeet(ctx context.Context, actor, id string) error {
	_, e := r.changeMeet(ctx, actor, id, "delete", 0)
	return e
}
