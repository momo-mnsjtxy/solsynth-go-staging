// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type storedEligibility interface {
	StoredSessionEligible(context.Context, *sql.DB, *sql.Tx, string, string, int64, int64, ...string) (bool, error)
}

func (r *Repository) eligible(ctx context.Context, tx *sql.Tx, actor, session string, version, epoch int64, permission string) (bool, error) {
	g, ok := any(r.guard).(storedEligibility)
	if !ok {
		return false, live.ErrUnavailable
	}
	var permissions []string
	if permission != "" {
		permissions = []string{permission}
	}
	return g.StoredSessionEligible(ctx, r.db, tx, actor, session, version, epoch, permissions...)
}
func (r *Repository) runAlive(ctx context.Context, tx *sql.Tx, v run) (bool, error) {
	ok, e := r.eligible(ctx, tx, v.Actor, v.Session, v.Version, v.Epoch, v.Permission)
	if e != nil || !ok {
		return ok, e
	}
	s, e := get(ctx, tx, v.Stream, false, true)
	if e != nil {
		return false, e
	}
	if s.DeletedAt != nil {
		return false, nil
	}
	current, e := observe(ctx, tx, s.PublisherID, "")
	if e != nil {
		return false, e
	}
	if current.owner != v.Owner {
		return false, nil
	}
	var version int64
	if e = tx.QueryRowContext(ctx, `SELECT version FROM owned_authority_accounts WHERE id=$1`, current.owner).Scan(&version); e != nil {
		return false, e
	}
	if version != v.OwnerVersion {
		return false, nil
	}
	editor, e := r.visible(ctx, tx, v.Actor, &s)
	if errors.Is(e, live.ErrNotFound) || errors.Is(e, live.ErrForbidden) {
		return false, nil
	}
	return editor, e
}

type admission struct {
	Actor, Session, Identity, State, Permission string
	Version, Epoch                              int64
	Publishing                                  bool
}

func admissions(ctx context.Context, tx *sql.Tx, v run) ([]admission, error) {
	rows, e := tx.QueryContext(ctx, `SELECT actor_id::text,session_id::text,identity::text,state,account_version,session_epoch,publishing,permission FROM owned_livestream_admissions WHERE stream_id=$1 AND generation=$2 AND state<>'removed' ORDER BY identity`, v.Stream, v.Generation)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []admission{}
	for rows.Next() {
		var a admission
		if e = rows.Scan(&a.Actor, &a.Session, &a.Identity, &a.State, &a.Version, &a.Epoch, &a.Publishing, &a.Permission); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (r *Repository) admissionAlive(ctx context.Context, tx *sql.Tx, s live.Stream, a admission) (bool, error) {
	ok, e := r.eligible(ctx, tx, a.Actor, a.Session, a.Version, a.Epoch, a.Permission)
	if e != nil || !ok {
		return ok, e
	}
	editor, e := r.visible(ctx, tx, a.Actor, &s)
	if errors.Is(e, live.ErrNotFound) || errors.Is(e, live.ErrForbidden) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if a.Publishing && !editor {
		return false, nil
	}
	return true, nil
}
func (r *Repository) systemLock(ctx context.Context, id string) (*sql.Tx, live.Stream, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return nil, live.Stream{}, e
	}
	fail := func(e error) (*sql.Tx, live.Stream, error) { tx.Rollback(); return nil, live.Stream{}, e }
	s, e := observe(ctx, tx, "", id)
	if e != nil {
		return fail(e)
	}
	if e = prelock(ctx, tx, "", id, s, true); e != nil {
		return fail(e)
	}
	if e = lockScope(ctx, tx, s, true); e != nil {
		return fail(e)
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_livestreams WHERE id=$1 FOR UPDATE NOWAIT`, id).Scan(&held); e != nil {
		return fail(e)
	}
	fresh, e := observe(ctx, tx, "", id)
	if e != nil {
		return fail(e)
	}
	if !same(s, fresh) {
		return fail(live.ErrConflict)
	}
	v, e := get(ctx, tx, id, false, true)
	if e != nil {
		return fail(e)
	}
	return tx, v, nil
}

// processRun only observes existing attempts or revokes provider resources. It
// never creates an ingress, token, user, service account or credential.
func (r *Repository) processRun(ctx context.Context, id string, generation int64) error {
	tx, stream, e := r.systemLock(ctx, id)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := loadRun(ctx, tx, id, generation)
	if e != nil {
		return e
	}
	if v.Phase == "done" {
		return tx.Commit()
	}
	if e = r.providerMatches(v); e != nil {
		return e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return e
	}
	if v.Phase != "ending" {
		alive, x := r.runAlive(ctx, tx, v)
		if x != nil {
			return x
		}
		if !alive || !v.ExpiresAt.After(at) || v.Phase == "attempted" && !v.CreatedAt.Add(r.cfg.IdleGrace).After(at) {
			if e = markEnd(ctx, tx, stream, false, ""); e != nil {
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
			return r.processRun(ctx, id, generation)
		}
	}
	if v.Phase == "ending" {
		return r.cleanup(ctx, tx, stream, v, at)
	}
	if v.Phase == "attempted" {
		if v.Mode != 0 {
			op, cancel := context.WithTimeout(ctx, 5*time.Second)
			ing, x := r.findIngress(op, v)
			cancel()
			if x != nil {
				return x
			}
			if ing != nil {
				if _, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_ingresses(stream_id,generation,ingress_id)VALUES($1,$2,$3)ON CONFLICT DO NOTHING`, id, generation, ing.ID); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_runs SET ingress_id=$3 WHERE stream_id=$1 AND generation=$2`, id, generation, ing.ID); e != nil {
					return e
				}
			}
		}
		return next(ctx, tx, v)
	}
	items, e := admissions(ctx, tx, v)
	if e != nil {
		return e
	}
	marked := false
	for _, a := range items {
		if a.State != "admitted" {
			continue
		}
		ok, x := r.admissionAlive(ctx, tx, stream, a)
		if x != nil {
			return x
		}
		if !ok {
			if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_admissions SET state='removing' WHERE identity=$1`, a.Identity); e != nil {
				return e
			}
			marked = true
		}
	}
	if marked {
		if e = tx.Commit(); e != nil {
			return e
		}
		return r.processRun(ctx, id, generation)
	}
	for _, a := range items {
		if a.State == "removing" {
			if e = r.remove(ctx, tx, v, a.Identity, at); e != nil {
				return e
			}
		}
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	participants, e := r.provider.StreamParticipants(op, v.Room)
	cancel()
	if e != nil {
		return live.ErrProvider
	}
	if len(participants) > 10000 {
		return live.ErrProvider
	}
	idleSince := v.CreatedAt
	if stream.StartedAt != nil {
		idleSince = *stream.StartedAt
	}
	if v.Mode != 0 && !idleSince.Add(r.cfg.IdleGrace).After(at) && len(participants) == 0 {
		if e = markEnd(ctx, tx, stream, false, ""); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return r.processRun(ctx, id, generation)
	}
	viewers := 0
	seen := map[string]bool{}
	for _, p := range participants {
		if seen[p.Identity] {
			continue
		}
		seen[p.Identity] = true
		for _, a := range items {
			if a.Identity == p.Identity && a.State == "admitted" && !a.Publishing {
				viewers++
				break
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestreams SET viewer_count=$2,peak_viewer_count=GREATEST(peak_viewer_count,$2) WHERE id=$1 AND generation=$3 AND status=1`, id, viewers, generation); e != nil {
		return e
	}
	return next(ctx, tx, v)
}
func next(ctx context.Context, tx *sql.Tx, v run) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_livestream_runs SET attempts=0,updated_at=clock_timestamp(),next_attempt_at=clock_timestamp()+interval '10 seconds' WHERE stream_id=$1 AND generation=$2`, v.Stream, v.Generation); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) remove(ctx context.Context, tx *sql.Tx, v run, identity string, at time.Time) error {
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	e := r.provider.Remove(op, v.Room, identity, at.Add(time.Second))
	cancel()
	if e != nil {
		return live.ErrProvider
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_livestream_admissions SET state='removed' WHERE identity=$1`, identity)
	return e
}
func (r *Repository) cleanup(ctx context.Context, tx *sql.Tx, s live.Stream, v run, at time.Time) error {
	// Persist each discovered provider ID before attempting deletion. A failed
	// DB commit after a provider acknowledgement can then safely retry its ID.
	if v.Mode != 0 {
		op, cancel := context.WithTimeout(ctx, 5*time.Second)
		items, e := r.provider.ListIngress(op, v.Room)
		cancel()
		if e != nil {
			return live.ErrProvider
		}
		ids := []string{}
		if v.Ingress != nil {
			ids = append(ids, *v.Ingress)
		}
		for _, ing := range items {
			if matches(v, ing) {
				ids = append(ids, ing.ID)
			}
		}
		added := false
		for _, id := range ids {
			result, e := tx.ExecContext(ctx, `INSERT INTO owned_livestream_ingresses(stream_id,generation,ingress_id)VALUES($1,$2,$3)ON CONFLICT DO NOTHING`, v.Stream, v.Generation, id)
			if e != nil {
				return e
			}
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			added = added || n > 0
		}
		if added {
			if e = tx.Commit(); e != nil {
				return e
			}
			return r.processRun(ctx, v.Stream, v.Generation)
		}
		rows, e := tx.QueryContext(ctx, `SELECT ingress_id,deleted_at FROM owned_livestream_ingresses WHERE stream_id=$1 AND generation=$2 ORDER BY ingress_id`, v.Stream, v.Generation)
		if e != nil {
			return e
		}
		type resource struct {
			id      string
			deleted *time.Time
		}
		resources := []resource{}
		for rows.Next() {
			var row resource
			if e = rows.Scan(&row.id, &row.deleted); e != nil {
				rows.Close()
				return e
			}
			resources = append(resources, row)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(resources) == 0 {
			return live.ErrConflict
		} // An unobserved create remains uncertain; never invent acknowledgement.
		for _, res := range resources {
			if res.deleted != nil {
				continue
			}
			op, cancel := context.WithTimeout(ctx, 5*time.Second)
			e = r.provider.DeleteIngress(op, res.id)
			cancel()
			if e != nil {
				return live.ErrProvider
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_ingresses SET deleted_at=clock_timestamp() WHERE stream_id=$1 AND generation=$2 AND ingress_id=$3`, v.Stream, v.Generation, res.id); e != nil {
				return e
			}
		}
	}
	items, e := admissions(ctx, tx, v)
	if e != nil {
		return e
	}
	for _, a := range items {
		if e = r.remove(ctx, tx, v, a.Identity, at); e != nil {
			return e
		}
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	e = r.provider.DeleteRoom(op, v.Room)
	cancel()
	if e != nil {
		return live.ErrProvider
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_runs SET phase='done',ingress_id=NULL,updated_at=clock_timestamp() WHERE stream_id=$1 AND generation=$2`, v.Stream, v.Generation); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestreams SET operation_state=CASE WHEN deleted_at IS NULL THEN 'ready' ELSE 'deleted' END,updated_at=clock_timestamp() WHERE id=$1 AND generation=$2`, v.Stream, v.Generation); e != nil {
		return e
	}
	return tx.Commit()
}

// ProcessDue is a bounded host-owned reconciliation hook. It starts no goroutine.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, live.ErrInvalid
	}
	if e := r.configured(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT stream_id::text,generation FROM owned_livestream_runs WHERE phase<>'done' AND provider=$2 AND endpoint=$3 AND next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at,stream_id,generation LIMIT $1`, limit, r.provider.Name(), r.provider.Endpoint())
	if e != nil {
		return 0, e
	}
	type key struct {
		id         string
		generation int64
	}
	keys := []key{}
	for rows.Next() {
		var k key
		if e = rows.Scan(&k.id, &k.generation); e != nil {
			rows.Close()
			return 0, e
		}
		keys = append(keys, k)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	var result error
	for _, k := range keys {
		if ctx.Err() != nil {
			return n, errors.Join(result, ctx.Err())
		}
		e = r.processRun(ctx, k.id, k.generation)
		if e == nil {
			n++
			continue
		}
		result = errors.Join(result, e)
		_, x := r.db.ExecContext(ctx, `UPDATE owned_livestream_runs SET attempts=LEAST(attempts+1,30),next_attempt_at=clock_timestamp()+LEAST(60,1<<LEAST(attempts,6))*interval '1 second' WHERE stream_id=$1 AND generation=$2 AND phase<>'done'`, k.id, k.generation)
		result = errors.Join(result, x)
	}
	if r.chat != nil {
		count, chatError := r.ProcessChatDue(ctx, limit)
		n += count
		result = errors.Join(result, chatError)
	}
	return n, result
}
