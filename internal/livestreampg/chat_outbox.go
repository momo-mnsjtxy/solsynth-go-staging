// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type chatEvent struct {
	ID, Stream, Actor, Session, Permission, Message, Kind, State, Lease string
	Generation, Version, Epoch                                          int64
	Payload, RecipientHash                                              []byte
	Expires                                                             time.Time
	LeaseExpires                                                        *time.Time
}

type chatDelivery struct {
	event      chatEvent
	room       string
	recipients []string
}

var errChatNoWork = errors.New("livestream chat event is not claimable")

func event(ctx context.Context, tx *sql.Tx, id string, lock bool) (v chatEvent, e error) {
	q := `SELECT id::text,stream_id::text,generation,actor_id::text,session_id::text,account_version,session_epoch,permission,COALESCE(message_id::text,''),kind,state,payload,expires_at,COALESCE(lease_token::text,''),lease_expires_at,recipient_hash FROM owned_livestream_chat_outbox WHERE id=$1`
	if lock {
		q += ` FOR UPDATE NOWAIT`
	}
	e = tx.QueryRowContext(ctx, q, id).Scan(&v.ID, &v.Stream, &v.Generation, &v.Actor, &v.Session, &v.Version, &v.Epoch, &v.Permission, &v.Message, &v.Kind, &v.State, &v.Payload, &v.Expires, &v.Lease, &v.LeaseExpires, &v.RecipientHash)
	return
}

func cancelChat(ctx context.Context, tx *sql.Tx, id, reason string) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET state='cancelled',reason=$2,lease_token=NULL,lease_expires_at=NULL,recipient_hash=NULL WHERE id=$1 AND state IN('pending','processing')`, id, reason); e != nil {
		return e
	}
	return tx.Commit()
}

// chatDeliveryState computes one current, explicitly addressed delivery while
// holding only database locks. A blank lease claims work; a nonblank lease
// performs the after-provider recheck and fenced acknowledgement.
func (r *Repository) chatDeliveryState(ctx context.Context, id, lease string) (chatDelivery, error) {
	var out chatDelivery
	tx, e := r.begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	observed, e := event(ctx, tx, id, false)
	if e != nil {
		return out, e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	if lease == "" {
		if observed.State != "pending" && !(observed.State == "processing" && observed.LeaseExpires != nil && !observed.LeaseExpires.After(at)) {
			return out, errChatNoWork
		}
	} else if observed.State != "processing" || observed.Lease != lease {
		return out, errChatNoWork
	}
	s, e := observe(ctx, tx, "", observed.Stream)
	if e != nil {
		return out, e
	}
	oldRun := run{Stream: observed.Stream, Generation: observed.Generation}
	before, e := admissions(ctx, tx, oldRun)
	if e != nil {
		return out, e
	}
	if len(before) > 100 {
		return out, live.ErrConflict
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
			return out, x
		}
		ids = append(ids, m.SenderID)
		locked[m.SenderID] = true
	}
	if e = prelock(ctx, tx, "", observed.Stream, s, true, ids...); e != nil {
		return out, e
	}
	if e = lockScope(ctx, tx, s, true); e != nil {
		return out, e
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_livestreams WHERE id=$1 FOR UPDATE NOWAIT`, observed.Stream).Scan(&held); e != nil {
		return out, e
	}
	fresh, e := observe(ctx, tx, "", observed.Stream)
	if e != nil {
		return out, e
	}
	if !same(s, fresh) {
		return out, live.ErrConflict
	}
	v, e := event(ctx, tx, id, true)
	if e != nil {
		return out, e
	}
	if lease == "" {
		if v.State != "pending" && !(v.State == "processing" && v.LeaseExpires != nil && !v.LeaseExpires.After(at)) {
			return out, errChatNoWork
		}
		var prior bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_livestream_chat_outbox a JOIN owned_livestream_chat_outbox b ON b.id=$1 WHERE a.stream_id=b.stream_id AND a.state IN('pending','processing') AND a.sequence<b.sequence)`, id).Scan(&prior); e != nil {
			return out, e
		}
		if prior {
			return out, errChatNoWork
		}
	} else if v.State != "processing" || v.Lease != lease {
		return out, errChatNoWork
	}
	stream, e := get(ctx, tx, v.Stream, false, true)
	if e != nil {
		return out, e
	}
	at, e = now(ctx, tx)
	if e != nil {
		return out, e
	}
	if stream.DeletedAt != nil || stream.Generation != v.Generation || stream.Status != live.Active || stream.OperationState != "active" || !v.Expires.After(at) {
		return out, cancelChat(ctx, tx, id, "stream_closed_or_expired")
	}
	run, e := loadRun(ctx, tx, v.Stream, v.Generation)
	if e != nil {
		return out, e
	}
	if e = r.providerMatches(run); e != nil {
		return out, e
	}
	if run.Phase != "active" || !run.ExpiresAt.After(at) {
		return out, cancelChat(ctx, tx, id, "stream_closed_or_expired")
	}
	alive, e := r.runAlive(ctx, tx, run)
	if e != nil {
		return out, e
	}
	if !alive {
		return out, cancelChat(ctx, tx, id, "stream_authority_revoked")
	}
	alive, e = r.eligible(ctx, tx, v.Actor, v.Session, v.Version, v.Epoch, v.Permission)
	if e != nil {
		return out, e
	}
	if !alive {
		return out, cancelChat(ctx, tx, id, "actor_authority_revoked")
	}
	editor, e := r.visible(ctx, tx, v.Actor, &stream)
	if errors.Is(e, live.ErrForbidden) || errors.Is(e, live.ErrNotFound) {
		return out, cancelChat(ctx, tx, id, "actor_visibility_revoked")
	}
	if e != nil {
		return out, e
	}
	if v.Kind == "timeout" && !editor {
		return out, cancelChat(ctx, tx, id, "moderator_role_revoked")
	}
	if v.Kind == "message" {
		m, x := chatMessage(ctx, tx, v.Stream, v.Message)
		if x != nil {
			return out, x
		}
		if m.DeletedAt != nil {
			return out, cancelChat(ctx, tx, id, "message_deleted")
		}
	}
	current, e := admissions(ctx, tx, run)
	if e != nil {
		return out, e
	}
	if len(current) > 100 {
		return out, live.ErrConflict
	}
	recipients := []string{}
	audienceChanged := false
	for _, a := range current {
		if a.State != "admitted" {
			continue
		}
		if !locked[a.Actor] {
			audienceChanged = true
			continue
		}
		ok, x := r.admissionAlive(ctx, tx, stream, a)
		if x != nil {
			return out, x
		}
		if ok {
			recipients = append(recipients, a.Identity)
		}
	}
	if len(recipients) == 0 {
		return out, cancelChat(ctx, tx, id, "no_eligible_recipients")
	}
	sort.Strings(recipients)
	recipientHash := hash(recipients)
	if lease != "" {
		if audienceChanged || !bytes.Equal(recipientHash, v.RecipientHash) {
			return out, cancelChat(ctx, tx, id, "audience_changed_after_send")
		}
		result, x := tx.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET state='accepted',accepted_at=clock_timestamp(),reason=NULL,lease_token=NULL,lease_expires_at=NULL,recipient_hash=NULL WHERE id=$1::uuid AND state='processing' AND lease_token=$2::uuid`, id, lease)
		if x != nil {
			return out, x
		}
		if n, x := result.RowsAffected(); x != nil || n != 1 {
			if x != nil {
				return out, x
			}
			return out, errChatNoWork
		}
		return chatDelivery{event: v, room: run.Room, recipients: recipients}, tx.Commit()
	}
	lease, e = uid()
	if e != nil {
		return out, e
	}
	leaseTTL := 3 * r.chat.SendTimeout
	if leaseTTL < 15*time.Second {
		leaseTTL = 15 * time.Second
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET state='processing',lease_token=$2::uuid,lease_expires_at=$3::timestamptz,recipient_hash=$4::bytea,attempts=attempts+1,reason='provider_pending' WHERE id=$1::uuid AND(state='pending' OR(state='processing' AND lease_expires_at<=clock_timestamp()))`, id, lease, at.Add(leaseTTL), recipientHash)
	if e != nil {
		return out, e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return out, e
		}
		return out, errChatNoWork
	}
	v.Lease = lease
	v.RecipientHash = recipientHash
	if e = tx.Commit(); e != nil {
		return out, e
	}
	return chatDelivery{event: v, room: run.Room, recipients: recipients}, nil
}

func (r *Repository) releaseChatLease(ctx context.Context, id, lease string) error {
	_, e := r.db.ExecContext(ctx, `UPDATE owned_livestream_chat_outbox SET state=CASE WHEN attempts >= $3 OR expires_at<=clock_timestamp() THEN 'failed' ELSE 'pending' END,reason='provider_uncertain',next_attempt_at=clock_timestamp()+LEAST(60,1<<LEAST(attempts,6))*interval '1 second',lease_token=NULL,lease_expires_at=NULL,recipient_hash=NULL WHERE id=$1::uuid AND state='processing' AND lease_token=$2::uuid`, id, lease, r.chat.MaxAttempts)
	return e
}

func (r *Repository) processChat(ctx context.Context, id string) (bool, error) {
	delivery, e := r.chatDeliveryState(ctx, id, "")
	if errors.Is(e, errChatNoWork) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	provider, ok := r.provider.(live.DataProvider)
	if !ok {
		_ = r.releaseChatLease(context.WithoutCancel(ctx), id, delivery.event.Lease)
		return false, live.ErrUnconfigured
	}
	op, cancel := context.WithTimeout(ctx, r.chat.SendTimeout)
	e = provider.SendStreamData(op, delivery.event.ID, delivery.room, delivery.recipients, delivery.event.Payload)
	cancel()
	if e != nil {
		x := r.releaseChatLease(context.WithoutCancel(ctx), id, delivery.event.Lease)
		return false, errors.Join(live.ErrProvider, x)
	}
	_, e = r.chatDeliveryState(ctx, id, delivery.event.Lease)
	if errors.Is(e, errChatNoWork) {
		return false, nil
	}
	return e == nil, e
}

func (r *Repository) ProcessChatDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, live.ErrInvalid
	}
	if e := r.chatConfigured(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT o.id::text FROM owned_livestream_chat_outbox o JOIN owned_livestream_runs r ON r.stream_id=o.stream_id AND r.generation=o.generation WHERE (o.state='pending' AND o.next_attempt_at<=clock_timestamp() OR o.state='processing' AND o.lease_expires_at<=clock_timestamp()) AND r.provider=$2 AND r.endpoint=$3 AND NOT EXISTS(SELECT 1 FROM owned_livestream_chat_outbox prior WHERE prior.stream_id=o.stream_id AND prior.state IN('pending','processing') AND prior.sequence<o.sequence) ORDER BY o.sequence LIMIT $1`, limit, r.provider.Name(), r.provider.Endpoint())
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
		processed, x := r.processChat(ctx, id)
		if processed {
			n++
		}
		result = errors.Join(result, x)
	}
	return n, result
}
