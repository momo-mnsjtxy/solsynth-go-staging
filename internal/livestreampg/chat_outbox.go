// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"errors"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"time"
)

type chatEvent struct {
	ID, Stream, Actor, Session, Permission, Message, Kind, State string
	Generation, Version, Epoch                                   int64
	Payload                                                      []byte
	Expires                                                      time.Time
}

func event(ctx context.Context, tx *sql.Tx, id string, lock bool) (v chatEvent, e error) {
	q := `SELECT id::text,stream_id::text,generation,actor_id::text,session_id::text,account_version,session_epoch,permission,COALESCE(message_id::text,''),kind,state,payload,expires_at FROM owned_livestream_chat_outbox WHERE id=$1`
	if lock {
		q += ` FOR UPDATE NOWAIT`
	}
	e = tx.QueryRowContext(ctx, q, id).Scan(&v.ID, &v.Stream, &v.Generation, &v.Actor, &v.Session, &v.Version, &v.Epoch, &v.Permission, &v.Message, &v.Kind, &v.State, &v.Payload, &v.Expires)
	return
}
func cancelChat(ctx context.Context, tx *sql.Tx, id, reason string) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET state='cancelled',reason=$2 WHERE id=$1 AND state='pending'`, id, reason); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) processChat(ctx context.Context, id string) error {
	tx, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	observed, e := event(ctx, tx, id, false)
	if e != nil {
		return e
	}
	if observed.State != "pending" {
		return tx.Commit()
	}
	s, e := observe(ctx, tx, "", observed.Stream)
	if e != nil {
		return e
	}
	// Only immutable IDs are needed for this nonlocking observation. The full
	// run is locked after accounts, publisher and stream below.
	oldRun := run{Stream: observed.Stream, Generation: observed.Generation}
	before, e := admissions(ctx, tx, oldRun)
	if e != nil {
		return e
	}
	if len(before) > 100 {
		return live.ErrConflict
	}
	ids := []string{observed.Actor}
	locked := map[string]bool{observed.Actor: true}
	for _, a := range before {
		ids = append(ids, a.Actor)
		locked[a.Actor] = true
	}
	if observed.Message != "" {
		m, x := chatMessage(ctx, tx, observed.Stream, observed.Message)
		if x != nil {
			return x
		}
		ids = append(ids, m.SenderID)
		locked[m.SenderID] = true
	}
	// All account rows, including recipients and the original event actor, precede
	// realm/publisher/stream/event locks. NOWAIT prevents maintenance lock inversion.
	if e = prelock(ctx, tx, "", observed.Stream, s, true, ids...); e != nil {
		return e
	}
	if e = lockScope(ctx, tx, s, true); e != nil {
		return e
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_livestreams WHERE id=$1 FOR UPDATE NOWAIT`, observed.Stream).Scan(&held); e != nil {
		return e
	}
	fresh, e := observe(ctx, tx, "", observed.Stream)
	if e != nil {
		return e
	}
	if !same(s, fresh) {
		return live.ErrConflict
	}
	v, e := event(ctx, tx, id, true)
	if e != nil {
		return e
	}
	if v.State != "pending" {
		return tx.Commit()
	}
	// Preserve per-stream enqueue order, including while earlier events back off.
	var prior bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_livestream_chat_outbox a JOIN owned_livestream_chat_outbox b ON b.id=$1 WHERE a.stream_id=b.stream_id AND a.state='pending' AND a.sequence<b.sequence)`, id).Scan(&prior); e != nil {
		return e
	}
	if prior {
		return tx.Commit()
	}
	stream, e := get(ctx, tx, v.Stream, false, true)
	if e != nil {
		return e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return e
	}
	if stream.DeletedAt != nil || stream.Generation != v.Generation || stream.Status != live.Active || stream.OperationState != "active" || !v.Expires.After(at) {
		return cancelChat(ctx, tx, id, "stream_closed_or_expired")
	}
	run, e := loadRun(ctx, tx, v.Stream, v.Generation)
	if e != nil {
		return e
	}
	if e = r.providerMatches(run); e != nil {
		return e
	}
	if run.Phase != "active" || !run.ExpiresAt.After(at) {
		return cancelChat(ctx, tx, id, "stream_closed_or_expired")
	}
	alive, e := r.runAlive(ctx, tx, run)
	if e != nil {
		return e
	}
	if !alive {
		return cancelChat(ctx, tx, id, "stream_authority_revoked")
	}
	alive, e = r.eligible(ctx, tx, v.Actor, v.Session, v.Version, v.Epoch, v.Permission)
	if e != nil {
		return e
	}
	if !alive {
		return cancelChat(ctx, tx, id, "actor_authority_revoked")
	}
	editor, e := r.visible(ctx, tx, v.Actor, &stream)
	if errors.Is(e, live.ErrForbidden) || errors.Is(e, live.ErrNotFound) {
		return cancelChat(ctx, tx, id, "actor_visibility_revoked")
	}
	if e != nil {
		return e
	}
	if v.Kind == "timeout" && !editor {
		return cancelChat(ctx, tx, id, "moderator_role_revoked")
	}
	if v.Kind == "message" {
		m, x := chatMessage(ctx, tx, v.Stream, v.Message)
		if x != nil {
			return x
		}
		if m.DeletedAt != nil {
			return cancelChat(ctx, tx, id, "message_deleted")
		}
	}
	current, e := admissions(ctx, tx, run)
	if e != nil {
		return e
	}
	if len(current) > 100 {
		return live.ErrConflict
	}
	recipients := []string{}
	for _, a := range current {
		// A join committed after the observation is excluded from this snapshot. It
		// reads persisted history; no late account lock may invert the global order.
		if a.State != "admitted" || !locked[a.Actor] {
			continue
		}
		ok, x := r.admissionAlive(ctx, tx, stream, a)
		if x != nil {
			return x
		}
		if ok {
			recipients = append(recipients, a.Identity)
		}
	}
	if len(recipients) == 0 {
		return cancelChat(ctx, tx, id, "no_eligible_recipients")
	}
	provider, ok := r.provider.(live.DataProvider)
	if !ok {
		return live.ErrUnconfigured
	}
	op, cancel := context.WithTimeout(ctx, r.chat.SendTimeout)
	e = provider.SendStreamData(op, run.Room, recipients, v.Payload)
	cancel()
	if e != nil {
		return live.ErrProvider
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET state='accepted',accepted_at=clock_timestamp(),reason=NULL,attempts=attempts+1 WHERE id=$1 AND state='pending'`, id); e != nil {
		return e
	}
	// Stable message/event IDs permit consumer deduplication if provider acceptance
	// succeeds but this commit fails. This deliberately makes no exactly-once claim.
	return tx.Commit()
}

func (r *Repository) ProcessChatDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, live.ErrInvalid
	}
	if e := r.chatConfigured(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT o.id::text FROM owned_livestream_chat_outbox o JOIN owned_livestream_runs r ON r.stream_id=o.stream_id AND r.generation=o.generation WHERE o.state='pending' AND o.next_attempt_at<=clock_timestamp() AND r.provider=$2 AND r.endpoint=$3 AND NOT EXISTS(SELECT 1 FROM owned_livestream_chat_outbox prior WHERE prior.stream_id=o.stream_id AND prior.state='pending' AND prior.sequence<o.sequence) ORDER BY o.sequence LIMIT $1`, limit, r.provider.Name(), r.provider.Endpoint())
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
		e = r.processChat(ctx, id)
		if e == nil {
			n++
			continue
		}
		result = errors.Join(result, e)
		// Failures never masquerade as provider acknowledgement. Retry is bounded by
		// both attempts and wall-clock expiry, with a maximum 60-second backoff.
		_, x := r.db.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET attempts=attempts+1,state=CASE WHEN attempts+1 >= $2 OR expires_at<=clock_timestamp() THEN 'failed' ELSE 'pending' END,reason='retry_failed',next_attempt_at=clock_timestamp()+LEAST(60,1<<LEAST(attempts,6))*interval '1 second' WHERE id=$1 AND state='pending'`, id, r.chat.MaxAttempts)
		result = errors.Join(result, x)
	}
	return n, result
}
