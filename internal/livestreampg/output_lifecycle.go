// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

func (r *Repository) outputAlive(ctx context.Context, tx *sql.Tx, v outputRecord, s live.Stream) (bool, error) {
	if s.Generation != v.Generation || s.Status != live.Active || s.OperationState != "active" || s.DeletedAt != nil || v.Relay && s.Visibility != live.Public {
		return false, nil
	}
	if v.Relay {
		if ok, e := r.relayVisible(ctx, tx, s); e != nil || !ok {
			return ok, e
		}
	}
	run, e := loadRun(ctx, tx, v.Stream, v.Generation)
	if e != nil {
		return false, e
	}
	if run.Phase != "active" || run.Room != v.Room || run.Owner != v.Owner || run.OwnerVersion != v.OwnerVersion {
		return false, nil
	}
	if ok, e := r.runAlive(ctx, tx, run); e != nil || !ok {
		return ok, e
	}
	if ok, e := r.eligible(ctx, tx, v.Actor, v.Session, v.Version, v.Epoch, v.Permission); e != nil || !ok {
		return ok, e
	}
	var super bool
	if e = tx.QueryRowContext(ctx, `SELECT is_superuser FROM owned_authority_accounts WHERE id=$1::uuid`, v.Actor).Scan(&super); e != nil || !super {
		return false, e
	}
	ok, e := r.publishers.CanEditPublisherInTransaction(ctx, tx, v.Actor, s.PublisherID)
	if e != nil || !ok {
		return ok, e
	}
	p, e := r.outputProvider()
	if e != nil {
		return false, e
	}
	fp, e := p.OutputPolicyFingerprint(v.Destinations, v.Kind)
	if errors.Is(e, live.ErrInvalid) || errors.Is(e, live.ErrOutputUnavailable) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return bytes.Equal(fp, v.PolicyHash), nil
}

// An external public relay must remain visible to a real anonymous viewer.
// Editor management exceptions (for example shadowbanned/private publishers)
// must not turn an externally readable destination into a privacy bypass.
func (r *Repository) relayVisible(ctx context.Context, tx *sql.Tx, s live.Stream) (bool, error) {
	if s.Visibility != live.Public {
		return false, nil
	}
	_, e := r.visible(ctx, tx, "", &s)
	if errors.Is(e, live.ErrNotFound) || errors.Is(e, live.ErrForbidden) {
		return false, nil
	}
	return e == nil, e
}

func (r *Repository) activateOutput(ctx context.Context, actor, id, sid, permission string) (result live.OutputResponse, observed live.ProviderOutput, e error) {
	tx, p, s, e := r.locked(ctx, actor, sid, "", permission, true, false)
	if e != nil {
		return
	}
	defer tx.Rollback()
	v, e := loadOutput(ctx, tx, id)
	if e != nil {
		return result, observed, e
	}
	if v.Actor != actor || v.Session != p.Session.ID || v.Version != p.Account.Version || v.Epoch != p.Session.AuthorizationEpoch || v.Phase != "attempted" || v.Egress != nil {
		return result, observed, live.ErrConflict
	}
	if ok, x := r.outputAlive(ctx, tx, v, s); x != nil || !ok {
		if x != nil {
			return result, observed, x
		}
		return result, observed, live.ErrForbidden
	}
	provider, e := r.outputProvider()
	if e != nil {
		return result, observed, e
	}
	op, cancel := context.WithTimeout(ctx, r.outputs.OperationTimeout)
	observed, e = provider.StartOutput(op, live.OutputStart{ID: v.ID, Room: v.Room, Kind: v.Kind, Destinations: v.Destinations, Playlist: v.Playlist, SegmentDuration: v.SegmentDuration, Layout: v.Layout})
	cancel()
	if observed.Room != v.Room || !live.ValidOutputProviderID(observed.ID) || observed.Absent {
		observed = live.ProviderOutput{}
		return result, observed, live.ErrProvider
	}
	if e != nil {
		return result, observed, e
	}
	if observed.Status < 0 || observed.Status > 6 {
		return result, observed, live.ErrProvider
	}
	at, e := now(ctx, tx)
	if e != nil {
		return result, observed, e
	}
	v.Egress = &observed.ID
	v.Started = &at
	v.Phase = "active"
	if observed.Status == 2 {
		v.Phase = "stopping"
	}
	if observed.Terminal() {
		v.Phase = "done"
		v.Finished = &at
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET egress_id=$2::text,phase=$3::text,started_at=$4::timestamptz,finished_at=$5::timestamptz,updated_at=$4::timestamptz,next_attempt_at=$4::timestamptz+interval '10 seconds' WHERE id=$1::uuid`, v.ID, observed.ID, v.Phase, at, v.Finished)
	if e != nil {
		return result, observed, e
	}
	if e = audit(ctx, tx, sid, actor, permission+".observed"); e != nil {
		return result, observed, e
	}
	if e = r.commit(ctx, tx, actor, permission); e != nil {
		return result, observed, e
	}
	return outputResponse(v), observed, nil
}

// Retaining a provider observation cannot authorize recording. It only adds a
// cleanup obligation after activation failed/rolled back. The activation tx has
// already closed, so no second connection waits behind its own domain locks.
func (r *Repository) rememberOutput(parent context.Context, id string, observed live.ProviderOutput) error {
	if !live.ValidOutputProviderID(observed.ID) || observed.Room == "" {
		return live.ErrProvider
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
	defer cancel()
	result, e := r.db.ExecContext(ctx, `UPDATE owned_livestream_outputs SET egress_id=COALESCE(egress_id,$2::text),phase=CASE WHEN phase='done' THEN 'done' ELSE 'stopping' END,reason='activation_unconfirmed',updated_at=clock_timestamp(),next_attempt_at=clock_timestamp() WHERE id=$1::uuid AND room_name=$3::text AND (egress_id IS NULL OR egress_id=$2::text)`, id, observed.ID, observed.Room)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return live.ErrProvider
	}
	return nil
}

func (r *Repository) StopOutput(ctx context.Context, actor, sid, kind string) error {
	if kind != "egress" && kind != "hls" {
		return live.ErrInvalid
	}
	if _, e := r.outputProvider(); e != nil {
		return e
	}
	permission := "livestreams." + kind
	tx, _, _, e := r.locked(ctx, actor, sid, "", permission, true, false)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := scanOutput(tx.QueryRowContext(ctx, `SELECT `+outputFields+` FROM owned_livestream_outputs WHERE stream_id=$1::uuid AND kind=$2::text AND phase<>'done' FOR UPDATE`, sid, kind))
	if errors.Is(e, live.ErrNotFound) {
		return r.commit(ctx, tx, actor, permission)
	}
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET phase='stopping',reason='requested_stop',updated_at=clock_timestamp(),next_attempt_at=clock_timestamp() WHERE id=$1::uuid`, v.ID); e != nil {
		return e
	}
	if e = audit(ctx, tx, sid, actor, permission+".stop.requested"); e != nil {
		return e
	}
	if e = r.commit(ctx, tx, actor, permission); e != nil {
		return e
	}
	if e = r.processOutput(ctx, v.ID); e != nil {
		return e
	}
	var done bool
	if e = r.db.QueryRowContext(ctx, `SELECT phase='done' FROM owned_livestream_outputs WHERE id=$1::uuid`, v.ID).Scan(&done); e != nil {
		return e
	}
	if !done {
		return live.ErrOutputUncertain
	}
	return nil
}

func (r *Repository) outputSystemLock(ctx context.Context, sid, actor string) (*sql.Tx, live.Stream, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return nil, live.Stream{}, e
	}
	fail := func(e error) (*sql.Tx, live.Stream, error) { tx.Rollback(); return nil, live.Stream{}, e }
	s, e := observe(ctx, tx, "", sid)
	if e != nil {
		return fail(e)
	}
	if e = prelock(ctx, tx, "", sid, s, true, actor); e != nil {
		return fail(e)
	}
	if e = lockScope(ctx, tx, s, true); e != nil {
		return fail(e)
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_livestreams WHERE id=$1::uuid FOR UPDATE NOWAIT`, sid).Scan(&held); e != nil {
		return fail(e)
	}
	fresh, e := observe(ctx, tx, "", sid)
	if e != nil {
		return fail(e)
	}
	if !same(s, fresh) {
		return fail(live.ErrConflict)
	}
	v, e := get(ctx, tx, sid, false, true)
	if e != nil {
		return fail(e)
	}
	return tx, v, nil
}

func (r *Repository) processOutput(ctx context.Context, id string) error {
	provider, e := r.outputProvider()
	if e != nil {
		return e
	}
	var sid, actor string
	if e = r.db.QueryRowContext(ctx, `SELECT stream_id::text,actor_id::text FROM owned_livestream_outputs WHERE id=$1::uuid`, id).Scan(&sid, &actor); e != nil {
		return e
	}
	tx, stream, e := r.outputSystemLock(ctx, sid, actor)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := loadOutput(ctx, tx, id)
	if e != nil {
		return e
	}
	if v.Phase == "done" {
		return tx.Commit()
	}
	if v.Provider != r.provider.Name() || v.Endpoint != r.provider.Endpoint() {
		return live.ErrOutputUnavailable
	}
	if v.Egress == nil {
		op, cancel := context.WithTimeout(ctx, r.outputs.OperationTimeout)
		items, e := provider.ListOutputs(op, v.Room)
		cancel()
		if e != nil {
			return e
		}
		matches := []live.ProviderOutput{}
		if v.Kind == "hls" && len(v.Destinations) == 1 {
			expected := "streaming/" + v.Destinations[0] + "/" + v.ID + "/" + v.Playlist
			for _, item := range items {
				if item.Room == v.Room && item.Playlist == expected && live.ValidOutputProviderID(item.ID) && !item.Absent && item.Status >= 0 && item.Status <= 6 {
					matches = append(matches, item)
				}
			}
		}
		if len(matches) != 1 {
			reason := "uncertain_start"
			if len(matches) > 1 {
				reason = "ambiguous_results"
			}
			_, e = tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET phase=CASE WHEN phase='stopping' THEN phase ELSE 'uncertain' END,reason=$2::text,updated_at=clock_timestamp(),next_attempt_at=clock_timestamp()+interval '30 seconds' WHERE id=$1::uuid`, id, reason)
			if e != nil {
				return e
			}
			return tx.Commit()
		}
		item := matches[0]
		phase := "stopping"
		var finished any
		if item.Terminal() {
			phase = "done"
			at, x := now(ctx, tx)
			if x != nil {
				return x
			}
			finished = at
		} else if v.Phase != "stopping" && item.Status < 2 {
			alive, x := r.outputAlive(ctx, tx, v, stream)
			if x != nil {
				return x
			}
			if alive {
				phase = "active"
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET egress_id=$2::text,phase=$3::text,reason='',started_at=COALESCE(started_at,clock_timestamp()),finished_at=$4::timestamptz,updated_at=clock_timestamp(),next_attempt_at=clock_timestamp() WHERE id=$1::uuid`, id, item.ID, phase, finished)
		if e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		if phase == "stopping" {
			return r.processOutput(ctx, id)
		}
		return nil
	}
	if v.Phase != "stopping" {
		alive, e := r.outputAlive(ctx, tx, v, stream)
		if e != nil {
			return e
		}
		if !alive {
			if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET phase='stopping',reason='authority_or_policy_revoked',updated_at=clock_timestamp(),next_attempt_at=clock_timestamp() WHERE id=$1::uuid`, id); e != nil {
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
			return r.processOutput(ctx, id)
		}
		op, cancel := context.WithTimeout(ctx, r.outputs.OperationTimeout)
		items, e := provider.ListOutputs(op, v.Room)
		cancel()
		if e != nil {
			return e
		}
		var found *live.ProviderOutput
		for _, item := range items {
			if item.ID == *v.Egress && item.Room == v.Room {
				cp := item
				found = &cp
				break
			}
		}
		if found != nil && found.Terminal() {
			return finishOutput(ctx, tx, id)
		}
		if found == nil || found.Status == 2 {
			if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET phase='stopping',reason='provider_completion_pending',updated_at=clock_timestamp(),next_attempt_at=clock_timestamp() WHERE id=$1::uuid`, id); e != nil {
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
			return r.processOutput(ctx, id)
		}
		return nextOutput(ctx, tx, id)
	}
	op, cancel := context.WithTimeout(ctx, r.outputs.OperationTimeout)
	observed, e := provider.StopOutput(op, v.Room, *v.Egress)
	cancel()
	if e != nil {
		return e
	}
	if observed.ID != *v.Egress || observed.Room != v.Room {
		return live.ErrProvider
	}
	if observed.Terminal() {
		return finishOutput(ctx, tx, id)
	}
	return nextOutput(ctx, tx, id)
}
func finishOutput(ctx context.Context, tx *sql.Tx, id string) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET phase='done',reason='',finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, id); e != nil {
		return e
	}
	return tx.Commit()
}
func nextOutput(ctx context.Context, tx *sql.Tx, id string) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_livestream_outputs SET attempts=0,updated_at=clock_timestamp(),next_attempt_at=clock_timestamp()+interval '10 seconds' WHERE id=$1::uuid`, id); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) ProcessOutputsDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, live.ErrInvalid
	}
	if _, e := r.outputProvider(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT id::text FROM owned_livestream_outputs WHERE phase<>'done' AND provider=$2::text AND endpoint=$3::text AND next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at,id LIMIT $1::integer`, limit, r.provider.Name(), r.provider.Endpoint())
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	var result error
	for _, id := range ids {
		if ctx.Err() != nil {
			return n, errors.Join(result, ctx.Err())
		}
		if e = r.processOutput(ctx, id); e == nil {
			n++
		} else {
			result = errors.Join(result, e)
			_, x := r.db.ExecContext(ctx, `UPDATE owned_livestream_outputs SET attempts=LEAST(attempts+1,30),reason='provider_or_state_pending',next_attempt_at=clock_timestamp()+LEAST(300,1<<LEAST(attempts,9))*interval '1 second' WHERE id=$1::uuid AND phase<>'done'`, id)
			result = errors.Join(result, x)
		}
	}
	return n, result
}
